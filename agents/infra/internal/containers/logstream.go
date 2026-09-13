package containers

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

// Stream ids of the Docker multiplexed stream (application/vnd.docker.multiplexed-stream).
const (
	StreamStdout = 1
	StreamStderr = 2
)

// maxFrameBytes bounds one frame payload; Docker splits log messages at 16 KiB.
const maxFrameBytes = 1 << 20

// ErrFrameTooLarge reports a frame header with an implausible payload size (not a multiplexed stream).
var ErrFrameTooLarge = errors.New("containers: log stream frame too large")

// FrameReader reads GET /containers/{id}/logs responses. Without a TTY the stream is
// multiplexed: every frame is an 8-byte header {stream, 0, 0, 0, size uint32 big endian}
// followed by size payload bytes. With a TTY (Raw) the body is the plain stdout stream.
type FrameReader struct {
	r   *bufio.Reader
	raw bool
	buf []byte
}

// NewFrameReader wraps a log stream body; raw is true for containers with a TTY.
func NewFrameReader(r io.Reader, raw bool) *FrameReader {
	return &FrameReader{r: bufio.NewReaderSize(r, 64<<10), raw: raw, buf: make([]byte, 32<<10)}
}

// Next returns the stream id and payload of the next frame. The payload is valid until the next call.
func (f *FrameReader) Next() (int, []byte, error) {
	if f.raw {
		n, err := f.r.Read(f.buf)
		if n > 0 {
			return StreamStdout, f.buf[:n], nil
		}
		if err == nil {
			err = io.ErrNoProgress
		}
		return 0, nil, err
	}
	var hdr [8]byte
	if _, err := io.ReadFull(f.r, hdr[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, io.EOF
		}
		return 0, nil, err
	}
	size := binary.BigEndian.Uint32(hdr[4:])
	if size > maxFrameBytes {
		return 0, nil, fmt.Errorf("%w: %d bytes", ErrFrameTooLarge, size)
	}
	if int(size) > cap(f.buf) {
		f.buf = make([]byte, size)
	}
	p := f.buf[:size]
	if _, err := io.ReadFull(f.r, p); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, nil, io.EOF
		}
		return 0, nil, err
	}
	stream := int(hdr[0])
	if stream != StreamStderr {
		stream = StreamStdout // 0 (stdin) never appears in logs; treat unknown ids as stdout
	}
	return stream, p, nil
}

// SplitTimestamp removes the RFC3339Nano prefix that timestamps=1 adds to every log line
// ("2026-09-14T10:00:00.123456789Z message"). ok is false when the line has no timestamp.
func SplitTimestamp(line []byte) (time.Time, []byte, bool) {
	for i, c := range line {
		if c == ' ' {
			t, err := time.Parse(time.RFC3339Nano, string(line[:i]))
			if err != nil {
				return time.Time{}, line, false
			}
			return t, line[i+1:], true
		}
		if i > 40 {
			break
		}
	}
	return time.Time{}, line, false
}

// SinceParam formats t for the since query parameter of the logs endpoint (seconds.nanoseconds).
func SinceParam(t time.Time) string {
	if t.IsZero() {
		return "0"
	}
	return strconv.FormatInt(t.Unix(), 10) + "." + fmt.Sprintf("%09d", t.Nanosecond())
}
