package sampler

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"testing"
)

func TestPerfHint(t *testing.T) {
	cases := []struct {
		name          string
		err           error
		paranoid      string
		readErr       error
		want, wantNot string
	}{
		{name: "paranoid 3 is the cause and says how to change it", err: syscall.EPERM, paranoid: "3",
			want: "sysctl -w kernel.perf_event_paranoid=2"},
		{name: "paranoid 4 is reported with its own value", err: syscall.EPERM, paranoid: "4",
			want: "kernel.perf_event_paranoid=4"},
		{name: "a permitting value says so rather than blaming the sysctl", err: syscall.EPERM, paranoid: "1",
			want: "not the reason", wantNot: "sysctl -w"},
		{name: "a permitting value points at the capabilities", err: syscall.EPERM, paranoid: "2",
			want: "EffectiveCapabilities"},
		{name: "unreadable sysctl says why", err: syscall.EPERM, readErr: os.ErrNotExist,
			want: "could not read " + ParanoidPath},
		{name: "unparsable value is quoted rather than guessed", err: syscall.EPERM, paranoid: "banana",
			want: `"banana"`},
		{name: "EACCES is explained too", err: syscall.EACCES, paranoid: "3", want: "perf_event_paranoid=3"},
		// Anything that is not a permission problem gets no guess: a wrapped ENODEV is its own answer.
		{name: "other errors get no hint", err: syscall.ENODEV, paranoid: "3", want: ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := PerfHint(c.err, c.paranoid, c.readErr)
			if c.want == "" {
				if got != "" {
					t.Fatalf("hint for a non-permission error = %q", got)
				}
				return
			}
			if !strings.Contains(got, c.want) {
				t.Fatalf("hint = %q, want it to contain %q", got, c.want)
			}
			if c.wantNot != "" && strings.Contains(got, c.wantNot) {
				t.Fatalf("hint = %q, want it NOT to contain %q", got, c.wantNot)
			}
		})
	}
}

// The error the operator sees is what matters, so the wrapping is checked end to end.
func TestPerfHintWrapsTheRealError(t *testing.T) {
	err := errors.New("permission denied")
	if h := PerfHint(err, "3", nil); h != "" {
		t.Fatalf("a plain error is not a permission error: %q", h)
	}
	if h := PerfHint(syscall.EPERM, "3", nil); !strings.HasPrefix(h, " [") || !strings.HasSuffix(h, "]") {
		t.Fatalf("hint is not bracketed for appending: %q", h)
	}
}

func TestReadParanoidBelowRoot(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(dir+"/proc/sys/kernel", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir+"/proc/sys/kernel/perf_event_paranoid", []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readParanoid(dir)
	if err != nil || got != "2" {
		t.Fatalf("= %q, %v", got, err)
	}
	if _, err := readParanoid(dir + "/nope"); err == nil {
		t.Fatal("a missing sysctl must be reported, not silently empty")
	}
}
