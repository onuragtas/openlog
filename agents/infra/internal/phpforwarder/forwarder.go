package phpforwarder

import (
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"os/user"
	"sync"
	"sync/atomic"
	"time"

	"github.com/onuragtas/openlog/agents/infra/internal/selfmon"

	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// DefaultFlushInterval is how long converted spans wait for more spans before they are handed to the pipeline.
const DefaultFlushInterval = time.Second

// Options configures a Forwarder.
type Options struct {
	Socket            string      // unix datagram socket path; empty disables the unix listener
	SocketGroup       string      // "" or "auto": first existing of AutoSocketGroups
	SocketMode        fs.FileMode // 0660 by default
	UDPListen         string      // host:port; empty disables UDP
	MaxPendingTraces  int
	ReassemblyTimeout time.Duration
	MaxBatchBytes     int           // payload size at which a batch is emitted early
	FlushInterval     time.Duration // default DefaultFlushInterval

	// HostResource is the infra agent's resource; its host.*, os.* and openlog.agent.* attributes are added.
	HostResource *resourcepb.Resource
	// Emit receives finished payloads (called serially, never concurrently). It must not retain td after enqueueing
	// a serialized copy.
	Emit  func(td *tracepb.TracesData, spans int)
	Stats *selfmon.Stats
	Log   *slog.Logger
	Now   func() time.Time

	// LookupGroup replaces user.LookupGroup (tests).
	LookupGroup func(string) (*user.Group, error)
}

// Forwarder is the php_forwarder module. Start and Stop may be called repeatedly (the module is switched on and
// off when discovery finds or loses a PHP runtime).
type Forwarder struct {
	o Options

	mu  sync.Mutex // guards asm
	asm *assembler

	emitMu sync.Mutex // guards batch and serializes Emit
	batch  *batcher

	lastReceived atomic.Int64 // unix nanoseconds of the last valid message

	runMu   sync.Mutex
	running bool
	unix    *net.UnixConn
	sock    socketInfo
	udp     *net.UDPConn
	stop    chan struct{}
	wg      sync.WaitGroup
}

// New returns a stopped forwarder.
func New(o Options) *Forwarder {
	if o.SocketMode == 0 {
		o.SocketMode = 0o660
	}
	if o.MaxPendingTraces <= 0 {
		o.MaxPendingTraces = 10000
	}
	if o.ReassemblyTimeout <= 0 {
		o.ReassemblyTimeout = 5 * time.Second
	}
	if o.MaxBatchBytes <= 0 {
		o.MaxBatchBytes = 1 << 20
	}
	if o.FlushInterval <= 0 {
		o.FlushInterval = DefaultFlushInterval
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Stats == nil {
		o.Stats = selfmon.New(time.Now())
	}
	if o.Emit == nil {
		o.Emit = func(*tracepb.TracesData, int) {}
	}
	return &Forwarder{
		o:     o,
		asm:   newAssembler(o.ReassemblyTimeout, o.MaxPendingTraces),
		batch: newBatcher(hostAttributes(o.HostResource), o.MaxBatchBytes),
	}
}

// Start binds the listeners and starts receiving. It fails when no listener could be bound.
func (f *Forwarder) Start() error {
	f.runMu.Lock()
	defer f.runMu.Unlock()
	if f.running {
		return nil
	}
	var errs []error
	if f.o.Socket != "" {
		conn, info, warn, err := listenUnix(f.o.Socket, f.o.SocketGroup, f.o.SocketMode, f.o.LookupGroup)
		if err != nil {
			errs = append(errs, err)
		} else {
			f.unix, f.sock = conn, info
			if warn != nil {
				f.o.Stats.PermissionDenied("php_forwarder")
				f.o.Log.Warn("php forwarder socket group", "socket", f.o.Socket, "warning", warn)
			}
		}
	}
	if f.o.UDPListen != "" {
		conn, err := listenUDP(f.o.UDPListen)
		if err != nil {
			errs = append(errs, err)
		} else {
			f.udp = conn
		}
	}
	if f.unix == nil && f.udp == nil {
		if len(errs) == 0 {
			errs = append(errs, errors.New("no listener configured"))
		}
		return errors.Join(errs...)
	}
	if len(errs) > 0 {
		f.o.Log.Error("php forwarder listener failed; continuing with the others", "error", errors.Join(errs...))
	}
	f.stop = make(chan struct{})
	f.running = true
	f.o.Stats.PHPStarted()
	if f.unix != nil {
		c := f.unix
		f.wg.Go(func() { f.readLoop(c) })
	}
	if f.udp != nil {
		c := f.udp
		f.wg.Go(func() { f.readLoop(c) })
	}
	f.wg.Go(f.sweepLoop)
	f.o.Log.Info("php forwarder started", "socket", f.o.Socket, "socket_group", f.sock.group,
		"socket_mode", "0"+formatOctal(f.o.SocketMode), "udp", f.o.UDPListen)
	return nil
}

// Stop closes the listeners, removes the socket and emits everything pending (unfinished traces as incomplete).
func (f *Forwarder) Stop() {
	f.runMu.Lock()
	defer f.runMu.Unlock()
	if !f.running {
		return
	}
	close(f.stop)
	if f.unix != nil {
		_ = f.unix.Close()
	}
	if f.udp != nil {
		_ = f.udp.Close()
	}
	f.wg.Wait()
	removeSocket(f.sock)
	f.unix, f.udp, f.sock = nil, nil, socketInfo{}
	f.running = false

	f.mu.Lock()
	traces := f.asm.flush()
	f.o.Stats.SetPHPPendingTraces(0)
	f.mu.Unlock()
	f.deliver(traces, true)
	f.o.Log.Info("php forwarder stopped", "flushed_incomplete_traces", len(traces))
}

// Running reports whether the listeners are active.
func (f *Forwarder) Running() bool {
	f.runMu.Lock()
	defer f.runMu.Unlock()
	return f.running
}

// LastReceived returns when the last valid message arrived (zero if never).
func (f *Forwarder) LastReceived() time.Time {
	if n := f.lastReceived.Load(); n != 0 {
		return time.Unix(0, n)
	}
	return time.Time{}
}

// SocketGroup returns the group applied to the socket ("" if none).
func (f *Forwarder) SocketGroup() string {
	f.runMu.Lock()
	defer f.runMu.Unlock()
	return f.sock.group
}

type datagramReader interface {
	Read(b []byte) (int, error)
}

func (f *Forwarder) readLoop(conn datagramReader) {
	buf := make([]byte, MaxDatagramBytes+1) // a longer datagram is truncated to len(buf) and rejected
	for {
		n, err := conn.Read(buf)
		if err != nil {
			select {
			case <-f.stop:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			f.o.Log.Debug("php forwarder read failed", "error", err)
			time.Sleep(10 * time.Millisecond)
			continue
		}
		f.Handle(buf[:n])
	}
}

func (f *Forwarder) sweepLoop() {
	t := time.NewTicker(min(f.o.FlushInterval, max(f.o.ReassemblyTimeout/5, 50*time.Millisecond)))
	defer t.Stop()
	lastFlush := f.o.Now()
	for {
		select {
		case <-f.stop:
			return
		case <-t.C:
		}
		now := f.o.Now()
		f.mu.Lock()
		expired := f.asm.expire(now)
		pending := f.asm.len()
		f.mu.Unlock()
		f.o.Stats.SetPHPPendingTraces(pending)
		if len(expired) > 0 {
			f.o.Stats.AddPHPReassemblyTimeouts(len(expired))
		}
		flush := now.Sub(lastFlush) >= f.o.FlushInterval*9/10 // ticker jitter must not delay a flush by a whole tick
		if flush {
			lastFlush = now
		}
		f.deliver(expired, flush)
	}
}

// Handle processes one datagram (exported for tests and embedding).
func (f *Forwarder) Handle(b []byte) {
	m, err := decode(b)
	switch {
	case errors.Is(err, errUnsupportedVersion):
		f.o.Stats.AddPHPMessages("unsupported_version", 1)
		return
	case err != nil:
		f.o.Stats.AddPHPMessages("malformed", 1)
		f.o.Log.Debug("php forwarder dropped a malformed message", "error", err)
		return
	}
	now := f.o.Now()
	f.lastReceived.Store(now.UnixNano())
	f.mu.Lock()
	t := f.asm.add(m, now)
	dropped := f.asm.takeDropped()
	pending := f.asm.len()
	f.mu.Unlock()
	f.o.Stats.AddPHPMessages("dropped", dropped)
	f.o.Stats.SetPHPPendingTraces(pending)
	if t != nil {
		f.deliver([]*trace{t}, false)
	}
}

// Flush expires overdue traces and emits the current batch (tests; the sweeper does this periodically).
func (f *Forwarder) Flush() {
	f.mu.Lock()
	expired := f.asm.expire(f.o.Now())
	pending := f.asm.len()
	f.mu.Unlock()
	f.o.Stats.SetPHPPendingTraces(pending)
	f.o.Stats.AddPHPReassemblyTimeouts(len(expired))
	f.deliver(expired, true)
}

// deliver converts traces into the batch and emits full batches (and the rest when flush is set).
func (f *Forwarder) deliver(traces []*trace, flush bool) {
	f.emitMu.Lock()
	defer f.emitMu.Unlock()
	var out []*tracepb.TracesData
	for _, t := range traces {
		f.o.Stats.AddPHPMessages("accepted", len(t.parts))
		out = append(out, f.batch.add(convertTrace(t))...)
	}
	if flush {
		if td := f.batch.take(); td != nil {
			out = append(out, td)
		}
	}
	for _, td := range out {
		n := SpanCount(td)
		f.o.Stats.AddPHPSpans(n)
		f.o.Emit(td, n)
	}
}

func formatOctal(m fs.FileMode) string {
	const digits = "01234567"
	p := uint32(m.Perm())
	return string([]byte{digits[p>>6&7], digits[p>>3&7], digits[p&7]})
}
