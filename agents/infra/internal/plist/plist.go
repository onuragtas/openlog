// Package plist decodes Apple property lists (binary "bplist00" and XML) into Go values: map[string]any,
// []any, string, int64, float64, bool, []byte and time.Time. The macOS agent reads package receipts,
// application Info.plist files and launchd job definitions with it; nothing is ever written.
package plist

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// MaxBytes bounds the input size accepted by Decode.
const MaxBytes = 8 << 20

const maxDepth = 64

var errFormat = errors.New("plist: malformed property list")

// appleEpoch is the reference date of plist dates (2001-01-01T00:00:00Z).
var appleEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

// Decode decodes a binary or XML property list.
func Decode(data []byte) (any, error) {
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("plist: %d bytes exceed the %d byte limit", len(data), MaxBytes)
	}
	if bytes.HasPrefix(data, []byte("bplist00")) {
		return decodeBinary(data)
	}
	return decodeXML(data)
}

// Dict decodes a property list whose top-level object is a dictionary.
func Dict(data []byte) (map[string]any, error) {
	v, err := Decode(data)
	if err != nil {
		return nil, err
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("plist: top-level object is not a dictionary")
	}
	return m, nil
}

// String returns m[key] when it is a string.
func String(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// --- binary ---

type binReader struct {
	data        []byte
	offsets     []uint64
	refSize     int
	visiting    map[uint64]bool
	objectCount uint64
}

func decodeBinary(data []byte) (any, error) {
	if len(data) < 8+32 {
		return nil, errFormat
	}
	trailer := data[len(data)-32:]
	offSize := int(trailer[6])
	refSize := int(trailer[7])
	numObjects := binary.BigEndian.Uint64(trailer[8:16])
	top := binary.BigEndian.Uint64(trailer[16:24])
	tableOff := binary.BigEndian.Uint64(trailer[24:32])
	if offSize < 1 || offSize > 8 || refSize < 1 || refSize > 8 || numObjects == 0 || top >= numObjects {
		return nil, errFormat
	}
	end := uint64(len(data) - 32)
	if tableOff < 8 || tableOff > end || numObjects > (end-tableOff)/uint64(offSize) {
		return nil, errFormat
	}
	r := &binReader{data: data[:end], refSize: refSize, visiting: map[uint64]bool{}, objectCount: numObjects}
	r.offsets = make([]uint64, numObjects)
	for i := uint64(0); i < numObjects; i++ {
		p := tableOff + i*uint64(offSize)
		r.offsets[i] = readUint(data[p : p+uint64(offSize)])
	}
	return r.object(top, 0)
}

func readUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func (r *binReader) object(ref uint64, depth int) (any, error) {
	if ref >= r.objectCount || depth > maxDepth || r.visiting[ref] {
		return nil, errFormat
	}
	off := r.offsets[ref]
	if off >= uint64(len(r.data)) {
		return nil, errFormat
	}
	marker := r.data[off]
	kind, low := marker>>4, marker&0x0f
	pos := off + 1
	switch kind {
	case 0x0:
		switch marker {
		case 0x08:
			return false, nil
		case 0x09:
			return true, nil
		}
		return nil, nil
	case 0x1:
		n := uint64(1) << low
		if n > 8 || pos+n > uint64(len(r.data)) {
			return nil, errFormat
		}
		return int64(readUint(r.data[pos : pos+n])), nil
	case 0x2:
		n := uint64(1) << low
		if pos+n > uint64(len(r.data)) {
			return nil, errFormat
		}
		switch n {
		case 4:
			return float64(math.Float32frombits(uint32(readUint(r.data[pos : pos+4])))), nil
		case 8:
			return math.Float64frombits(readUint(r.data[pos : pos+8])), nil
		}
		return nil, errFormat
	case 0x3:
		if marker != 0x33 || pos+8 > uint64(len(r.data)) {
			return nil, errFormat
		}
		secs := math.Float64frombits(readUint(r.data[pos : pos+8]))
		return appleEpoch.Add(time.Duration(secs * float64(time.Second))), nil
	case 0x4, 0x5, 0x6, 0xA, 0xD:
		count, next, err := r.length(low, pos)
		if err != nil {
			return nil, err
		}
		pos = next
		switch kind {
		case 0x4:
			if pos+count > uint64(len(r.data)) {
				return nil, errFormat
			}
			return append([]byte(nil), r.data[pos:pos+count]...), nil
		case 0x5:
			if pos+count > uint64(len(r.data)) {
				return nil, errFormat
			}
			return string(r.data[pos : pos+count]), nil
		case 0x6:
			if count > uint64(len(r.data)) || pos+2*count > uint64(len(r.data)) {
				return nil, errFormat
			}
			u := make([]uint16, count)
			for i := range u {
				u[i] = binary.BigEndian.Uint16(r.data[pos+uint64(2*i):])
			}
			return string(utf16.Decode(u)), nil
		}
		r.visiting[ref] = true
		defer delete(r.visiting, ref)
		refs := func(start, n uint64) ([]uint64, error) {
			if n > uint64(len(r.data)) || start+n*uint64(r.refSize) > uint64(len(r.data)) {
				return nil, errFormat
			}
			out := make([]uint64, n)
			for i := range out {
				p := start + uint64(i*r.refSize)
				out[i] = readUint(r.data[p : p+uint64(r.refSize)])
			}
			return out, nil
		}
		if kind == 0xA {
			rs, err := refs(pos, count)
			if err != nil {
				return nil, err
			}
			arr := make([]any, 0, len(rs))
			for _, x := range rs {
				v, err := r.object(x, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			return arr, nil
		}
		keys, err := refs(pos, count)
		if err != nil {
			return nil, err
		}
		vals, err := refs(pos+count*uint64(r.refSize), count)
		if err != nil {
			return nil, err
		}
		m := make(map[string]any, count)
		for i := range keys {
			k, err := r.object(keys[i], depth+1)
			if err != nil {
				return nil, err
			}
			ks, ok := k.(string)
			if !ok {
				return nil, errFormat
			}
			v, err := r.object(vals[i], depth+1)
			if err != nil {
				return nil, err
			}
			m[ks] = v
		}
		return m, nil
	case 0x8:
		n := uint64(low) + 1
		if pos+n > uint64(len(r.data)) {
			return nil, errFormat
		}
		return int64(readUint(r.data[pos : pos+n])), nil
	}
	return nil, errFormat
}

// length decodes the object length: the low nibble, or an int object that follows when it is 0xF.
func (r *binReader) length(low byte, pos uint64) (uint64, uint64, error) {
	if low != 0x0f {
		return uint64(low), pos, nil
	}
	if pos >= uint64(len(r.data)) || r.data[pos]>>4 != 0x1 {
		return 0, 0, errFormat
	}
	n := uint64(1) << (r.data[pos] & 0x0f)
	if n > 8 || pos+1+n > uint64(len(r.data)) {
		return 0, 0, errFormat
	}
	return readUint(r.data[pos+1 : pos+1+n]), pos + 1 + n, nil
}

// --- XML ---

func decodeXML(data []byte) (any, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, errFormat
			}
			return nil, fmt.Errorf("plist: %w", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			if se.Name.Local == "plist" {
				continue
			}
			return xmlValue(dec, se, 0)
		}
	}
}

func xmlValue(dec *xml.Decoder, se xml.StartElement, depth int) (any, error) {
	if depth > maxDepth {
		return nil, errFormat
	}
	switch se.Name.Local {
	case "dict":
		m := map[string]any{}
		key := ""
		haveKey := false
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, errFormat
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "key" {
					s, err := xmlText(dec)
					if err != nil {
						return nil, err
					}
					key, haveKey = s, true
					continue
				}
				v, err := xmlValue(dec, t, depth+1)
				if err != nil {
					return nil, err
				}
				if haveKey {
					m[key] = v
					haveKey = false
				}
			case xml.EndElement:
				return m, nil
			}
		}
	case "array":
		var arr []any
		for {
			tok, err := dec.Token()
			if err != nil {
				return nil, errFormat
			}
			switch t := tok.(type) {
			case xml.StartElement:
				v, err := xmlValue(dec, t, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			case xml.EndElement:
				return arr, nil
			}
		}
	case "true", "false":
		if err := dec.Skip(); err != nil {
			return nil, errFormat
		}
		return se.Name.Local == "true", nil
	}
	s, err := xmlText(dec)
	if err != nil {
		return nil, err
	}
	switch se.Name.Local {
	case "string":
		return s, nil
	case "integer":
		v, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
		if err != nil {
			return nil, errFormat
		}
		return v, nil
	case "real":
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return nil, errFormat
		}
		return v, nil
	case "date":
		t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
		if err != nil {
			return nil, errFormat
		}
		return t, nil
	case "data":
		b, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(s), ""))
		if err != nil {
			return nil, errFormat
		}
		return b, nil
	}
	return nil, nil
}

// xmlText reads character data up to the end of the current element.
func xmlText(dec *xml.Decoder) (string, error) {
	var sb strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			return "", errFormat
		}
		switch t := tok.(type) {
		case xml.CharData:
			sb.Write(t)
		case xml.EndElement:
			return sb.String(), nil
		case xml.StartElement:
			return "", errFormat
		}
	}
}
