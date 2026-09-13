// Package buffer implements an on-disk FIFO queue of payloads that could not
// be exported. Each entry is one file written via temp file + rename, so a
// crash never leaves a partially written entry visible.
package buffer

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
)

const suffix = ".pb"

// Entry describes one buffered payload.
type Entry struct {
	Name   string
	Seq    uint64
	Signal string
	Items  int
	Size   int64
}

// Buffer is a size-bounded FIFO of payload files.
type Buffer struct {
	dir      string
	maxBytes int64

	mu      sync.Mutex
	entries []Entry
	size    int64
	seq     uint64
}

// Open opens (or creates) a buffer directory and indexes existing entries.
// maxBytes <= 0 disables buffering: every Push is dropped.
func Open(dir string, maxBytes int64) (*Buffer, error) {
	b := &Buffer{dir: dir, maxBytes: maxBytes}
	if maxBytes <= 0 {
		return b, nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("buffer: %w", err)
	}
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("buffer: %w", err)
	}
	for _, de := range des {
		name := de.Name()
		if strings.HasSuffix(name, ".tmp") {
			_ = os.Remove(filepath.Join(dir, name)) // interrupted write
			continue
		}
		e, ok := parseName(name)
		if !ok {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		e.Size = info.Size()
		b.entries = append(b.entries, e)
		b.size += e.Size
		b.seq = max(b.seq, e.Seq)
	}
	sort.Slice(b.entries, func(i, j int) bool { return b.entries[i].Seq < b.entries[j].Seq })
	return b, nil
}

// <seq>-<signal>-<items>.pb
func parseName(name string) (Entry, bool) {
	base, ok := strings.CutSuffix(name, suffix)
	if !ok {
		return Entry{}, false
	}
	parts := strings.Split(base, "-")
	if len(parts) != 3 {
		return Entry{}, false
	}
	seq, err1 := strconv.ParseUint(parts[0], 10, 64)
	items, err2 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || parts[1] == "" {
		return Entry{}, false
	}
	return Entry{Name: name, Seq: seq, Signal: parts[1], Items: items}, true
}

// NextSeq reserves the next sequence number. Payloads that wait in memory
// reserve their number up front so that a later PushSeq keeps global FIFO order.
func (b *Buffer) NextSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.seq++
	return b.seq
}

// Push appends a payload with a new sequence number. It returns the entries
// dropped to stay within the size limit (the oldest ones, or the new payload
// itself if it cannot fit).
func (b *Buffer) Push(signal string, items int, data []byte) ([]Entry, error) {
	dropped, _, err := b.PushSeq(b.NextSeq(), signal, items, data)
	return dropped, err
}

// PushSeq stores a payload under a sequence number obtained from NextSeq.
// Entries are kept ordered by sequence number; when the size limit is exceeded
// the entries with the lowest numbers (the oldest payloads, possibly the new
// one) are dropped and returned. stored reports whether the payload remains.
func (b *Buffer) PushSeq(seq uint64, signal string, items int, data []byte) (dropped []Entry, stored bool, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	size := int64(len(data))
	e := Entry{Seq: seq, Signal: signal, Items: items, Size: size}
	e.Name = fmt.Sprintf("%020d-%s-%d%s", seq, signal, items, suffix)
	if b.maxBytes <= 0 || size > b.maxBytes {
		return []Entry{e}, false, nil
	}
	b.seq = max(b.seq, seq)
	final := filepath.Join(b.dir, e.Name)
	tmp := final + ".tmp"
	if err := writeSync(tmp, data); err != nil {
		_ = os.Remove(tmp)
		return []Entry{e}, false, fmt.Errorf("buffer: write: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return []Entry{e}, false, fmt.Errorf("buffer: rename: %w", err)
	}
	i := sort.Search(len(b.entries), func(i int) bool { return b.entries[i].Seq > seq })
	b.entries = append(b.entries, Entry{})
	copy(b.entries[i+1:], b.entries[i:])
	b.entries[i] = e
	b.size += size
	stored = true
	for b.size > b.maxBytes && len(b.entries) > 0 {
		old := b.entries[0]
		if err := os.Remove(filepath.Join(b.dir, old.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return dropped, stored, fmt.Errorf("buffer: drop %s: %w", old.Name, err)
		}
		b.entries = b.entries[1:]
		b.size -= old.Size
		dropped = append(dropped, old)
		if old.Seq == seq {
			stored = false
		}
	}
	return dropped, stored, nil
}

func writeSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Peek returns the oldest entry and its content. Unreadable entries are
// discarded and reported via the dropped slice.
func (b *Buffer) Peek() (e Entry, data []byte, ok bool, dropped []Entry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for len(b.entries) > 0 {
		e = b.entries[0]
		d, err := os.ReadFile(filepath.Join(b.dir, e.Name))
		if err == nil {
			return e, d, true, dropped
		}
		b.entries = b.entries[1:]
		b.size -= e.Size
		dropped = append(dropped, e)
	}
	return Entry{}, nil, false, dropped
}

// Remove deletes an entry previously returned by Peek.
func (b *Buffer) Remove(e Entry) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i, cur := range b.entries {
		if cur.Seq == e.Seq {
			b.entries = append(b.entries[:i], b.entries[i+1:]...)
			b.size -= cur.Size
			break
		}
	}
	if err := os.Remove(filepath.Join(b.dir, e.Name)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Len returns the number of buffered entries.
func (b *Buffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.entries)
}

// Bytes returns the total size of buffered entries.
func (b *Buffer) Bytes() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.size
}
