// Package rpmdb reads installed packages from the rpm database without cgo or
// librpm. It contains a minimal read-only SQLite table reader (enough for
// rpmdb.sqlite, including committed WAL frames) and an rpm header parser.
package rpmdb

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strings"
)

var errCorrupt = errors.New("rpmdb: malformed sqlite file")

// sqliteDB is a read-only snapshot of a SQLite database file.
type sqliteDB struct {
	data     []byte
	pageSize int
	usable   int
	wal      map[uint32][]byte // page number → latest committed WAL version
}

func openSQLite(file string) (*sqliteDB, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	wal, _ := os.ReadFile(file + "-wal")
	return parseSQLite(data, wal)
}

func parseSQLite(data, wal []byte) (*sqliteDB, error) {
	if len(data) < 100 || !bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
		return nil, errors.New("rpmdb: not a sqlite database")
	}
	ps := int(binary.BigEndian.Uint16(data[16:18]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps&(ps-1) != 0 {
		return nil, errCorrupt
	}
	if enc := binary.BigEndian.Uint32(data[56:60]); enc > 1 {
		return nil, fmt.Errorf("rpmdb: unsupported text encoding %d", enc)
	}
	db := &sqliteDB{data: data, pageSize: ps, usable: ps - int(data[20])}
	db.wal = parseWAL(wal, ps)
	return db, nil
}

// parseWAL returns the pages of all committed transactions in a WAL file.
// Frames after the last commit frame, or with a different salt (a previous
// WAL generation), are ignored.
func parseWAL(wal []byte, pageSize int) map[uint32][]byte {
	if len(wal) < 32 {
		return nil
	}
	magic := binary.BigEndian.Uint32(wal[0:4])
	if magic != 0x377f0682 && magic != 0x377f0683 {
		return nil
	}
	if int(binary.BigEndian.Uint32(wal[8:12])) != pageSize {
		return nil
	}
	salt1, salt2 := binary.BigEndian.Uint32(wal[16:20]), binary.BigEndian.Uint32(wal[20:24])
	committed := map[uint32][]byte{}
	pending := map[uint32][]byte{}
	for off := 32; off+24+pageSize <= len(wal); off += 24 + pageSize {
		fh := wal[off : off+24]
		if binary.BigEndian.Uint32(fh[8:12]) != salt1 || binary.BigEndian.Uint32(fh[12:16]) != salt2 {
			break
		}
		pg := binary.BigEndian.Uint32(fh[0:4])
		pending[pg] = wal[off+24 : off+24+pageSize]
		if binary.BigEndian.Uint32(fh[4:8]) != 0 { // commit frame
			for k, v := range pending {
				committed[k] = v
			}
			clear(pending)
		}
	}
	return committed
}

func (db *sqliteDB) page(n uint32) ([]byte, error) {
	if n == 0 {
		return nil, errCorrupt
	}
	if p, ok := db.wal[n]; ok {
		return p, nil
	}
	start := int(n-1) * db.pageSize
	if start+db.pageSize > len(db.data) {
		return nil, errCorrupt
	}
	return db.data[start : start+db.pageSize], nil
}

// scanTable calls fn with the record payload of every row of the table
// b-tree rooted at root.
func (db *sqliteDB) scanTable(root uint32, fn func(rowid int64, payload []byte) error) error {
	visited := map[uint32]bool{}
	var walk func(pg uint32, depth int) error
	walk = func(pg uint32, depth int) error {
		if depth > 32 || visited[pg] {
			return errCorrupt
		}
		visited[pg] = true
		p, err := db.page(pg)
		if err != nil {
			return err
		}
		hdr := 0
		if pg == 1 {
			hdr = 100
		}
		if hdr+8 > len(p) {
			return errCorrupt
		}
		kind := p[hdr]
		ncells := int(binary.BigEndian.Uint16(p[hdr+3 : hdr+5]))
		ptrs := hdr + 8
		if kind == 0x05 {
			ptrs = hdr + 12
		}
		if ptrs+2*ncells > len(p) {
			return errCorrupt
		}
		for i := 0; i < ncells; i++ {
			cell := int(binary.BigEndian.Uint16(p[ptrs+2*i:]))
			if cell >= len(p) {
				return errCorrupt
			}
			switch kind {
			case 0x05: // interior table page: 4-byte left child, rowid
				if cell+4 > len(p) {
					return errCorrupt
				}
				if err := walk(binary.BigEndian.Uint32(p[cell:]), depth+1); err != nil {
					return err
				}
			case 0x0d: // leaf table page
				payload, rowid, err := db.leafPayload(p, cell)
				if err != nil {
					return err
				}
				if err := fn(rowid, payload); err != nil {
					return err
				}
			default:
				return errCorrupt
			}
		}
		if kind == 0x05 {
			return walk(binary.BigEndian.Uint32(p[hdr+8:]), depth+1)
		}
		return nil
	}
	return walk(root, 0)
}

func (db *sqliteDB) leafPayload(p []byte, off int) ([]byte, int64, error) {
	size, n := varint(p[off:])
	if n == 0 {
		return nil, 0, errCorrupt
	}
	off += n
	rowid, n := varint(p[off:])
	if n == 0 {
		return nil, 0, errCorrupt
	}
	off += n
	total := int(size)
	if total < 0 || total > 256<<20 {
		return nil, 0, errCorrupt
	}
	u := db.usable
	maxLocal := u - 35
	local := total
	if total > maxLocal {
		minLocal := ((u-12)*32)/255 - 23
		k := minLocal + (total-minLocal)%(u-4)
		if k <= maxLocal {
			local = k
		} else {
			local = minLocal
		}
	}
	if off+local > len(p) {
		return nil, 0, errCorrupt
	}
	out := make([]byte, 0, total)
	out = append(out, p[off:off+local]...)
	if local == total {
		return out, int64(rowid), nil
	}
	if off+local+4 > len(p) {
		return nil, 0, errCorrupt
	}
	next := binary.BigEndian.Uint32(p[off+local:])
	for hops := 0; len(out) < total; hops++ {
		if next == 0 || hops > 1<<20 {
			return nil, 0, errCorrupt
		}
		op, err := db.page(next)
		if err != nil {
			return nil, 0, err
		}
		next = binary.BigEndian.Uint32(op[0:4])
		chunk := min(total-len(out), u-4)
		out = append(out, op[4:4+chunk]...)
	}
	return out, int64(rowid), nil
}

// varint decodes a SQLite variable-length integer; n is 0 on error.
func varint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < 9; i++ {
		if i >= len(b) {
			return 0, 0
		}
		if i == 8 {
			return v<<8 | uint64(b[i]), 9
		}
		v = v<<7 | uint64(b[i]&0x7f)
		if b[i] < 0x80 {
			return v, i + 1
		}
	}
	return 0, 0
}

// column is one decoded record value (only the types rpmdb needs).
type column struct {
	isNull bool
	i      int64
	b      []byte // text or blob
}

func decodeRecord(payload []byte) ([]column, error) {
	hsize, n := varint(payload)
	if n == 0 || int(hsize) > len(payload) || int(hsize) < n {
		return nil, errCorrupt
	}
	var types []uint64
	for pos := n; pos < int(hsize); {
		t, m := varint(payload[pos:int(hsize)])
		if m == 0 {
			return nil, errCorrupt
		}
		types = append(types, t)
		pos += m
	}
	cols := make([]column, 0, len(types))
	body := payload[hsize:]
	for _, t := range types {
		var c column
		var size int
		switch {
		case t == 0:
			c.isNull = true
		case t >= 1 && t <= 6:
			size = []int{0, 1, 2, 3, 4, 6, 8}[t]
			if len(body) < size {
				return nil, errCorrupt
			}
			var v int64
			for i := 0; i < size; i++ {
				v = v<<8 | int64(body[i])
			}
			shift := uint(64 - 8*size) // sign-extend
			c.i = v << shift >> shift
		case t == 7:
			size = 8
		case t == 8, t == 9:
			c.i = int64(t - 8)
		case t >= 12:
			size = int((t - 12) / 2)
			if len(body) < size {
				return nil, errCorrupt
			}
			c.b = body[:size]
		default:
			return nil, errCorrupt
		}
		if len(body) < size {
			return nil, errCorrupt
		}
		body = body[size:]
		cols = append(cols, c)
	}
	return cols, nil
}

// tableRoot finds the root page of a table in sqlite_schema (page 1).
func (db *sqliteDB) tableRoot(name string) (uint32, error) {
	var root uint32
	err := db.scanTable(1, func(_ int64, payload []byte) error {
		cols, err := decodeRecord(payload)
		if err != nil || len(cols) < 4 {
			return err
		}
		if string(cols[0].b) == "table" && strings.EqualFold(string(cols[1].b), name) {
			root = uint32(cols[3].i)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if root == 0 {
		return 0, fmt.Errorf("rpmdb: table %s not found", name)
	}
	return root, nil
}
