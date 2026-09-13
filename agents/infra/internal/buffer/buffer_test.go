package buffer

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFIFOAndReopen(t *testing.T) {
	dir := t.TempDir()
	b, err := Open(dir, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range []string{"aaa", "bbbb", "cc"} {
		if dropped, err := b.Push("logs", i+1, []byte(s)); err != nil || len(dropped) != 0 {
			t.Fatalf("push: %v %v", dropped, err)
		}
	}
	if b.Len() != 3 || b.Bytes() != 9 {
		t.Fatalf("len=%d bytes=%d", b.Len(), b.Bytes())
	}
	// Leftover temp file from a crash must be ignored and cleaned.
	if err := os.WriteFile(filepath.Join(dir, "00000000000000000099-logs-1.pb.tmp"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}

	b2, err := Open(dir, 1000)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for {
		e, data, ok, _ := b2.Peek()
		if !ok {
			break
		}
		got = append(got, string(data))
		if e.Signal != "logs" || e.Items != len(got) {
			t.Errorf("entry = %+v", e)
		}
		if err := b2.Remove(e); err != nil {
			t.Fatal(err)
		}
	}
	if len(got) != 3 || got[0] != "aaa" || got[1] != "bbbb" || got[2] != "cc" || b2.Bytes() != 0 {
		t.Errorf("replay = %v bytes=%d", got, b2.Bytes())
	}
	if _, err := os.Stat(filepath.Join(dir, "00000000000000000099-logs-1.pb.tmp")); !os.IsNotExist(err) {
		t.Error("tmp file not cleaned")
	}
	// Sequence continues after reopen.
	if _, err := b2.Push("metrics", 1, []byte("z")); err != nil {
		t.Fatal(err)
	}
	e, _, _, _ := b2.Peek()
	if e.Seq != 4 {
		t.Errorf("seq = %d", e.Seq)
	}
}

func TestDropOldest(t *testing.T) {
	b, err := Open(t.TempDir(), 10)
	if err != nil {
		t.Fatal(err)
	}
	b.Push("metrics", 5, []byte("12345"))
	b.Push("metrics", 3, []byte("123"))
	dropped, err := b.Push("logs", 7, []byte("1234567"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dropped) != 1 || dropped[0].Items != 5 || b.Len() != 2 || b.Bytes() != 10 {
		t.Errorf("dropped=%v len=%d bytes=%d", dropped, b.Len(), b.Bytes())
	}
	dropped, _ = b.Push("logs", 2, []byte("this payload is too large"))
	if len(dropped) != 1 || dropped[0].Items != 2 || b.Len() != 2 {
		t.Errorf("oversized: dropped=%v len=%d", dropped, b.Len())
	}
}

func TestDisabled(t *testing.T) {
	b, err := Open("", 0)
	if err != nil {
		t.Fatal(err)
	}
	dropped, err := b.Push("logs", 4, []byte("x"))
	if err != nil || len(dropped) != 1 || b.Len() != 0 {
		t.Errorf("disabled: %v %v", dropped, err)
	}
}

func TestPushSeqKeepsOrderAndDropsOldest(t *testing.T) {
	b, err := Open(t.TempDir(), 9)
	if err != nil {
		t.Fatal(err)
	}
	s1, s2, s3 := b.NextSeq(), b.NextSeq(), b.NextSeq()
	// A payload that waited in memory (s1) is persisted after newer ones.
	if _, stored, _ := b.PushSeq(s3, "metrics", 3, []byte("ccc")); !stored {
		t.Fatal("s3 not stored")
	}
	if _, stored, _ := b.PushSeq(s2, "metrics", 2, []byte("bbb")); !stored {
		t.Fatal("s2 not stored")
	}
	if _, stored, _ := b.PushSeq(s1, "logs", 1, []byte("aaa")); !stored {
		t.Fatal("s1 not stored")
	}
	e, data, ok, _ := b.Peek()
	if !ok || e.Seq != s1 || string(data) != "aaa" {
		t.Fatalf("head = %+v %q", e, data)
	}
	// Over the limit: the oldest (lowest sequence) entry is dropped, even if
	// the payload being pushed is newer.
	dropped, stored, err := b.PushSeq(b.NextSeq(), "metrics", 4, []byte("ddd"))
	if err != nil || !stored || len(dropped) != 1 || dropped[0].Seq != s1 {
		t.Errorf("dropped = %+v stored = %v err = %v", dropped, stored, err)
	}
	// An older payload than everything stored is itself the oldest: dropped.
	b2, _ := Open(t.TempDir(), 6)
	old := b2.NextSeq()
	b2.PushSeq(b2.NextSeq(), "m", 1, []byte("xxx"))
	b2.PushSeq(b2.NextSeq(), "m", 1, []byte("yyy"))
	dropped, stored, _ = b2.PushSeq(old, "m", 1, []byte("zzz"))
	if stored || len(dropped) != 1 || dropped[0].Seq != old || b2.Len() != 2 {
		t.Errorf("old payload: dropped = %+v stored = %v len = %d", dropped, stored, b2.Len())
	}
}
