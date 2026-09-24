package phpfpm

// A minimal FastCGI responder client. PHP-FPM speaks FastCGI, not HTTP: its status page is served by the
// master process for the request whose SCRIPT_NAME equals pm.status_path, so reading it means speaking the
// protocol. Go's net/http/fcgi is the server half only, and the client half needed here is one request with
// no body — records in, records out — so it is written out rather than pulled in as a dependency.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
)

// Record types (RFC-less; FastCGI 1.0 specification §8).
const (
	typeBeginRequest = 1
	typeEndRequest   = 3
	typeParams       = 4
	typeStdin        = 5
	typeStdout       = 6
	typeStderr       = 7
)

// roleResponder is the only role the status page needs.
const roleResponder = 1

// requestID is fixed: one request per connection, and the connection is not reused.
const requestID = 1

const (
	headerLen = 8
	// maxContent is the protocol's per-record limit (uint16).
	maxContent = 65535
	// maxResponse bounds the accumulated stdout of one request; a status page is a few KiB.
	maxResponse = 1 << 20
	// maxRecords bounds the records read, so a peer that never sends END_REQUEST cannot spin forever.
	maxRecords = 4096
)

// errNotFastCGI reports a peer that does not speak FastCGI (e.g. an HTTP server on the probed port).
var errNotFastCGI = errors.New("the endpoint does not speak FastCGI")

type header struct {
	Version       uint8
	Type          uint8
	RequestID     uint16
	ContentLength uint16
	PaddingLength uint8
	Reserved      uint8
}

// fcgiDo performs one responder request on conn and returns the raw stdout stream — CGI headers included,
// so the caller can read the Status header before trusting the body — and the stderr stream separately:
// PHP-FPM reports a refused status request there.
func fcgiDo(ctx context.Context, conn net.Conn, params map[string]string) (body []byte, stderr []byte, err error) {
	if d, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(d)
	}
	var out bytes.Buffer
	if err := writeBeginRequest(&out); err != nil {
		return nil, nil, err
	}
	if err := writeParams(&out, params); err != nil {
		return nil, nil, err
	}
	// An empty PARAMS record closes the stream; an empty STDIN says the request has no body.
	if err := writeRecord(&out, typeParams, nil); err != nil {
		return nil, nil, err
	}
	if err := writeRecord(&out, typeStdin, nil); err != nil {
		return nil, nil, err
	}
	if _, err := conn.Write(out.Bytes()); err != nil {
		return nil, nil, err
	}

	var stdout, errOut bytes.Buffer
	r := io.LimitReader(conn, maxResponse+headerLen*maxRecords)
	for i := 0; ; i++ {
		if i >= maxRecords {
			return nil, nil, fmt.Errorf("%w: no end of request after %d records", errNotFastCGI, maxRecords)
		}
		var h header
		if err := binary.Read(r, binary.BigEndian, &h); err != nil {
			if errors.Is(err, io.EOF) && stdout.Len() > 0 {
				break // a peer that closed after its output
			}
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, nil, fmt.Errorf("%w: connection closed without a response", errNotFastCGI)
			}
			return nil, nil, err
		}
		if h.Version != 1 {
			return nil, nil, fmt.Errorf("%w: record version %d", errNotFastCGI, h.Version)
		}
		content := make([]byte, int(h.ContentLength)+int(h.PaddingLength))
		if _, err := io.ReadFull(r, content); err != nil {
			return nil, nil, fmt.Errorf("%w: truncated record: %v", errNotFastCGI, err)
		}
		content = content[:h.ContentLength]
		switch h.Type {
		case typeStdout:
			if stdout.Len()+len(content) > maxResponse {
				return nil, nil, fmt.Errorf("the status response is larger than %d bytes", maxResponse)
			}
			stdout.Write(content)
		case typeStderr:
			errOut.Write(content)
		case typeEndRequest:
			return stdout.Bytes(), errOut.Bytes(), nil
		}
	}
	return stdout.Bytes(), errOut.Bytes(), nil
}

func writeRecord(w io.Writer, typ uint8, content []byte) error {
	for {
		n := len(content)
		if n > maxContent {
			n = maxContent
		}
		h := header{Version: 1, Type: typ, RequestID: requestID, ContentLength: uint16(n)}
		if err := binary.Write(w, binary.BigEndian, h); err != nil {
			return err
		}
		if n > 0 {
			if _, err := w.Write(content[:n]); err != nil {
				return err
			}
		}
		content = content[n:]
		// A zero-length record is the stream terminator: write it once, never loop on it.
		if len(content) == 0 {
			return nil
		}
	}
}

func writeBeginRequest(w io.Writer) error {
	var body [8]byte
	binary.BigEndian.PutUint16(body[0:2], roleResponder)
	// flags = 0: the connection is closed after the response, which is what a one-shot read wants.
	return writeRecord(w, typeBeginRequest, body[:])
}

// writeParams encodes the name-value pairs in a stable order, so a request is reproducible in tests.
func writeParams(w io.Writer, params map[string]string) error {
	var buf bytes.Buffer
	for _, k := range sortedKeys(params) {
		writeLen(&buf, len(k))
		writeLen(&buf, len(params[k]))
		buf.WriteString(k)
		buf.WriteString(params[k])
	}
	if buf.Len() == 0 {
		return nil
	}
	return writeRecord(w, typeParams, buf.Bytes())
}

// writeLen writes a name-value length: one byte below 128, else four bytes with the high bit set.
func writeLen(buf *bytes.Buffer, n int) {
	if n < 128 {
		buf.WriteByte(byte(n))
		return
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(n)|1<<31)
	buf.Write(b[:])
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// splitCGI drops the CGI headers a responder writes before its body.
func splitCGI(b []byte) []byte {
	if i := bytes.Index(b, []byte("\r\n\r\n")); i >= 0 {
		return b[i+4:]
	}
	if i := bytes.Index(b, []byte("\n\n")); i >= 0 {
		return b[i+2:]
	}
	return b
}

// cgiStatus reads the Status header of a CGI response (200 when absent).
func cgiStatus(b []byte) int {
	end := bytes.Index(b, []byte("\r\n\r\n"))
	if end < 0 {
		if end = bytes.Index(b, []byte("\n\n")); end < 0 {
			return 200
		}
	}
	for _, line := range strings.Split(string(b[:end]), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "status") {
			continue
		}
		code, _, _ := strings.Cut(strings.TrimSpace(value), " ")
		if n, err := strconv.Atoi(code); err == nil {
			return n
		}
	}
	return 200
}
