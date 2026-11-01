package symbols

import (
	"debug/dwarf"
	"debug/elf"
	"debug/macho"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Image is one binary reduced to what a stack needs. ID is what a device reports for it: a Mach-O UUID or an ELF build id,
// in lower-case hex with no dashes.
type Image struct {
	ID    string
	Table *Table
}

// NormalizeImageID is how every id is compared: "A1B2-C3" and "a1b2c3" are the same image.
func NormalizeImageID(id string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(id), "-", ""))
}

// FromMachO reads one Mach-O file, usually the DWARF file inside a dSYM. A universal file yields one image per architecture.
func FromMachO(path string) ([]Image, error) {
	if fat, err := macho.OpenFat(path); err == nil {
		defer fat.Close()
		var out []Image
		for _, a := range fat.Arches {
			img, err := imageFromMachO(a.File)
			if err != nil {
				return nil, err
			}
			out = append(out, img)
		}
		return out, nil
	}
	f, err := macho.Open(path)
	if err != nil {
		return nil, fmt.Errorf("symbols: %s: %w", path, err)
	}
	defer f.Close()
	img, err := imageFromMachO(f)
	if err != nil {
		return nil, err
	}
	return []Image{img}, nil
}

const lcUUID = 0x1b

func imageFromMachO(f *macho.File) (Image, error) {
	var id string
	for _, l := range f.Loads {
		raw := l.Raw()
		if len(raw) >= 24 && f.ByteOrder.Uint32(raw) == lcUUID {
			id = hex.EncodeToString(raw[8:24])
		}
	}
	if id == "" {
		return Image{}, errors.New("symbols: the file has no UUID")
	}
	// MetricKit reports offsets into the __TEXT segment, while DWARF and the symbol table hold virtual addresses.
	var base uint64
	if text := f.Segment("__TEXT"); text != nil {
		base = text.Addr
	}
	t := &Table{}
	if d, err := f.DWARF(); err == nil {
		readDWARF(d, base, t)
	}
	if f.Symtab != nil {
		for _, s := range f.Symtab.Syms {
			// N_STAB entries describe debug info and N_TYPE == N_SECT is a defined symbol: only those are code here.
			if s.Type&0xe0 != 0 || s.Type&0x0e != 0x0e || s.Value < base || s.Sect == 0 {
				continue
			}
			t.funcs = append(t.funcs, function{s.Value - base, 0, strings.TrimPrefix(s.Name, "_")})
		}
	}
	t.finish()
	return Image{id, t}, nil
}

// FromELF reads one ELF file: an Android .so, or Unity's .sym.so of it. A device's rel_pc is an ELF virtual address.
func FromELF(path string) (Image, error) {
	f, err := elf.Open(path)
	if err != nil {
		return Image{}, fmt.Errorf("symbols: %s: %w", path, err)
	}
	defer f.Close()
	id := elfBuildID(f)
	if id == "" {
		return Image{}, fmt.Errorf("symbols: %s has no build id", path)
	}
	t := &Table{}
	if d, err := f.DWARF(); err == nil {
		readDWARF(d, 0, t)
	}
	add := func(syms []elf.Symbol, err error) {
		if err != nil {
			return
		}
		for _, s := range syms {
			if elf.ST_TYPE(s.Info) == elf.STT_FUNC && s.Value != 0 {
				t.funcs = append(t.funcs, function{s.Value, s.Value + s.Size, s.Name})
			}
		}
	}
	add(f.Symbols())
	add(f.DynamicSymbols())
	t.finish()
	return Image{id, t}, nil
}

func elfBuildID(f *elf.File) string {
	for _, s := range f.Sections {
		if s.Type != elf.SHT_NOTE {
			continue
		}
		data, err := s.Data()
		if err != nil {
			continue
		}
		for len(data) >= 12 {
			nameSize := f.ByteOrder.Uint32(data)
			descSize := f.ByteOrder.Uint32(data[4:])
			kind := f.ByteOrder.Uint32(data[8:])
			nameEnd := 12 + align4(nameSize)
			descEnd := nameEnd + align4(descSize)
			if uint64(descEnd) > uint64(len(data)) {
				break
			}
			if kind == 3 && nameSize == 4 && string(data[12:15]) == "GNU" { // NT_GNU_BUILD_ID
				return hex.EncodeToString(data[nameEnd : nameEnd+descSize])
			}
			data = data[descEnd:]
		}
	}
	return ""
}

func align4(n uint32) uint32 { return (n + 3) &^ 3 }

// readDWARF adds what the debug info knows. A problem partway leaves what was read so far: a stack that resolves most of its
// frames is worth more than an upload refused over one compilation unit.
func readDWARF(d *dwarf.Data, base uint64, t *Table) {
	files := map[string]int32{}
	fileIndex := func(name string) int32 {
		if i, ok := files[name]; ok {
			return i
		}
		i := int32(len(t.files))
		files[name] = i
		t.files = append(t.files, name)
		return i
	}
	r := d.Reader()
	for {
		e, err := r.Next()
		if err != nil || e == nil {
			return
		}
		switch e.Tag {
		case dwarf.TagCompileUnit:
			lr, err := d.LineReader(e)
			if err != nil || lr == nil {
				continue
			}
			var le dwarf.LineEntry
			for {
				if err := lr.Next(&le); err != nil {
					break // the end of the table, or a broken one: what was read stands either way
				}
				// An end of sequence marks the address after the last instruction, which may be where the next function starts.
				if le.EndSequence || le.File == nil || le.Address < base {
					continue
				}
				t.lines = append(t.lines, lineEntry{le.Address - base, fileIndex(le.File.Name), int32(le.Line)})
			}
		case dwarf.TagSubprogram:
			ranges, err := d.Ranges(e)
			if err != nil || len(ranges) == 0 {
				continue
			}
			name := subprogramName(d, e)
			if name == "" {
				continue
			}
			for _, rg := range ranges {
				if rg[0] >= base && rg[1] > rg[0] {
					t.funcs = append(t.funcs, function{rg[0] - base, rg[1] - base, name})
				}
			}
		}
	}
}

// subprogramName follows the reference an out-of-line copy of an inlined or declared function carries to the entry with the name.
func subprogramName(d *dwarf.Data, e *dwarf.Entry) string {
	for depth := 0; depth < 4 && e != nil; depth++ {
		if n, ok := e.Val(dwarf.AttrLinkageName).(string); ok && n != "" {
			return n
		}
		if n, ok := e.Val(dwarf.AttrName).(string); ok && n != "" {
			return n
		}
		off, ok := e.Val(dwarf.AttrAbstractOrigin).(dwarf.Offset)
		if !ok {
			off, ok = e.Val(dwarf.AttrSpecification).(dwarf.Offset)
		}
		if !ok {
			return ""
		}
		r := d.Reader()
		r.Seek(off)
		next, err := r.Next()
		if err != nil {
			return ""
		}
		e = next
	}
	return ""
}

// IsELF and IsMachO decide how a file is read from its first bytes, so the CLI can be pointed at a directory.
func IsELF(path string) bool {
	return hasMagic(path, func(b []byte) bool { return string(b) == "\x7fELF" })
}

func IsMachO(path string) bool {
	return hasMagic(path, func(b []byte) bool {
		switch binary.BigEndian.Uint32(b) {
		case 0xfeedface, 0xfeedfacf, 0xcefaedfe, 0xcffaedfe, 0xcafebabe:
			return true
		}
		return false
	})
}

func hasMagic(path string, ok func([]byte) bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	b := make([]byte, 4)
	if _, err := io.ReadFull(f, b); err != nil {
		return false
	}
	return ok(b)
}
