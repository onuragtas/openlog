package containers

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// Containers of CRI runtimes (containerd, CRI-O) are listed through the Kubernetes Container Runtime
// Interface (runtime.v1.RuntimeService) on the runtime's unix socket. gRPC is spoken directly: unary calls
// are HTTP/2 POSTs with length-prefixed protobuf messages (net/http's unencrypted HTTP/2, protowire), so the
// agent needs neither google.golang.org/grpc nor k8s.io/cri-api. Only the few fields used below are decoded.

// LogDriverCRI is the LogDriver of CRI containers: the log file is in the CRI log format
// ("<RFC3339Nano> stdout|stderr F|P <message>").
const LogDriverCRI = "cri"

// CRI calls and limits.
const (
	criService     = "/runtime.v1.RuntimeService/"
	criMaxResponse = 64 << 20
	// criSkipFor is how long a socket that does not serve the CRI (e.g. Docker's own containerd, whose CRI
	// plugin is disabled) is not asked again.
	criSkipFor = 5 * time.Minute
)

// errCRIUnimplemented means the socket answers gRPC but has no runtime.v1.RuntimeService.
var errCRIUnimplemented = errors.New("containers: CRI runtime.v1 not served")

// CRI container states (runtime.v1.ContainerState).
var criStates = map[uint64]string{0: "created", 1: "running", 2: "exited", 3: "unknown"}

// criTransport is an HTTP/2 (prior knowledge, no TLS) transport over a unix socket.
func criTransport(sock string) *http.Transport {
	var p http.Protocols
	p.SetUnencryptedHTTP2(true)
	return &http.Transport{
		Protocols: &p,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
}

// grpcStatusError is a non-OK gRPC status.
type grpcStatusError struct {
	code int
	msg  string
}

func (e *grpcStatusError) Error() string { return fmt.Sprintf("grpc status %d: %s", e.code, e.msg) }

// criCall performs one unary gRPC call and returns the response message.
func criCall(ctx context.Context, tr http.RoundTripper, method string, msg []byte) ([]byte, error) {
	body := make([]byte, 5, 5+len(msg))
	binary.BigEndian.PutUint32(body[1:], uint32(len(msg)))
	body = append(body, msg...)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost"+criService+method, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("TE", "trailers")
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, criMaxResponse+5))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("CRI %s: HTTP %d", method, resp.StatusCode)
	}
	// Trailers-only responses carry the status in the headers.
	status, message := resp.Trailer.Get("Grpc-Status"), resp.Trailer.Get("Grpc-Message")
	if status == "" {
		status, message = resp.Header.Get("Grpc-Status"), resp.Header.Get("Grpc-Message")
	}
	if status != "0" {
		code, err := strconv.Atoi(status)
		if err != nil {
			return nil, fmt.Errorf("CRI %s: missing grpc-status", method)
		}
		if m, err := url.PathUnescape(message); err == nil {
			message = m
		}
		if code == 12 { // UNIMPLEMENTED
			return nil, fmt.Errorf("%w: %s", errCRIUnimplemented, message)
		}
		return nil, fmt.Errorf("CRI %s: %w", method, &grpcStatusError{code: code, msg: message})
	}
	if len(data) < 5 {
		return nil, fmt.Errorf("CRI %s: empty response", method)
	}
	if data[0] != 0 {
		return nil, fmt.Errorf("CRI %s: compressed response not supported", method)
	}
	n := binary.BigEndian.Uint32(data[1:5])
	if n > criMaxResponse || int(n) > len(data)-5 {
		return nil, fmt.Errorf("CRI %s: response message of %d bytes", method, n)
	}
	return data[5 : 5+n], nil
}

// protoFields calls fn for every field of a protobuf message: v holds length-delimited contents,
// x varint values.
func protoFields(b []byte, fn func(num protowire.Number, typ protowire.Type, v []byte, x uint64)) error {
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return protowire.ParseError(n)
		}
		b = b[n:]
		switch typ {
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(b)
			if n < 0 {
				return protowire.ParseError(n)
			}
			fn(num, typ, v, 0)
			b = b[n:]
		case protowire.VarintType:
			x, n := protowire.ConsumeVarint(b)
			if n < 0 {
				return protowire.ParseError(n)
			}
			fn(num, typ, nil, x)
			b = b[n:]
		default:
			n := protowire.ConsumeFieldValue(num, typ, b)
			if n < 0 {
				return protowire.ParseError(n)
			}
			b = b[n:]
		}
	}
	return nil
}

// protoMapEntry decodes a map<string,string> entry.
func protoMapEntry(b []byte) (k, v string, err error) {
	err = protoFields(b, func(num protowire.Number, typ protowire.Type, val []byte, _ uint64) {
		if typ != protowire.BytesType {
			return
		}
		switch num {
		case 1:
			k = string(val)
		case 2:
			v = string(val)
		}
	})
	return k, v, err
}

// criImage decodes an ImageSpec: image (1) and user_specified_image (18).
func criImage(b []byte) (image, userSpecified string, err error) {
	err = protoFields(b, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if typ != protowire.BytesType {
			return
		}
		switch num {
		case 1:
			image = string(v)
		case 18:
			userSpecified = string(v)
		}
	})
	return image, userSpecified, err
}

// criRestartAnnotation is the kubelet annotation holding the restart count of a container.
const criRestartAnnotation = "io.kubernetes.container.restartCount"

// ParseCRIList parses a runtime.v1 ListContainersResponse into containers of the given runtime.
func ParseCRIList(msg []byte, runtime string) ([]Container, error) {
	var out []Container
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	err := protoFields(msg, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num != 1 || typ != protowire.BytesType {
			return
		}
		c, err := parseCRIContainer(v, runtime)
		keep(err)
		if err == nil && c.ID != "" {
			out = append(out, c)
		}
	})
	keep(err)
	if firstErr != nil {
		return nil, fmt.Errorf("containers: decode CRI container list: %w", firstErr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func parseCRIContainer(b []byte, runtime string) (Container, error) {
	c := Container{Runtime: runtime, State: "created", Labels: map[string]string{}, Ports: []Port{}}
	labels := map[string]string{}
	var imageID, imageRef, specImage, userImage string
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	err := protoFields(b, func(num protowire.Number, typ protowire.Type, v []byte, x uint64) {
		switch {
		case typ == protowire.VarintType && num == 6: // state
			if s, ok := criStates[x]; ok {
				c.State = s
			} else {
				c.State = "unknown"
			}
		case typ == protowire.VarintType && num == 7: // created_at (ns)
			if ns := int64(x); ns > 0 {
				c.Created = time.Unix(0, ns).UTC().Format(time.RFC3339)
			}
		case typ != protowire.BytesType:
		case num == 1:
			c.ID = string(v)
		case num == 3: // metadata
			keep(protoFields(v, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
				if num == 1 && typ == protowire.BytesType {
					c.Name = string(v)
				}
			}))
		case num == 4:
			var err error
			specImage, userImage, err = criImage(v)
			keep(err)
		case num == 5:
			imageRef = string(v)
		case num == 8:
			k, val, err := protoMapEntry(v)
			keep(err)
			labels[k] = val
		case num == 9:
			k, val, err := protoMapEntry(v)
			keep(err)
			if k == criRestartAnnotation {
				c.RestartCount, _ = strconv.Atoi(val)
			}
		case num == 10:
			imageID = string(v)
		}
	})
	keep(err)
	if firstErr != nil {
		return Container{}, firstErr
	}
	switch {
	case userImage != "":
		c.Image = userImage
	default:
		c.Image = specImage
	}
	c.ImageID = imageID
	if c.ImageID == "" {
		c.ImageID = imageRef
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		if i >= MaxLabels {
			break
		}
		c.Labels[k] = truncate(labels[k], MaxLabelValueBytes)
	}
	return c, nil
}

// CRIStatus holds the fields of a runtime.v1 ContainerStatusResponse used by the agent.
type CRIStatus struct {
	Details
	// Image is the image reference the runtime resolved (e.g. a repo tag where the list has an image id).
	Image string
}

// ParseCRIStatus parses a runtime.v1 ContainerStatusResponse (verbose=false).
func ParseCRIStatus(msg []byte) (CRIStatus, error) {
	var st CRIStatus
	var firstErr error
	keep := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	err := protoFields(msg, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num != 1 || typ != protowire.BytesType {
			return
		}
		keep(protoFields(v, func(num protowire.Number, typ protowire.Type, v []byte, x uint64) {
			switch {
			case typ == protowire.VarintType && num == 5:
				st.StartedAt = nsTime(x)
			case typ == protowire.VarintType && num == 6:
				st.FinishedAt = nsTime(x)
			case typ == protowire.VarintType && num == 7:
				st.ExitCode = int(int32(x))
			case typ != protowire.BytesType:
			case num == 8:
				image, user, err := criImage(v)
				keep(err)
				st.Image = image
				if user != "" {
					st.Image = user
				}
			case num == 13:
				k, val, err := protoMapEntry(v)
				keep(err)
				if k == criRestartAnnotation {
					st.RestartCount, _ = strconv.Atoi(val)
				}
			case num == 15:
				st.LogPath = string(v)
			}
		}))
	})
	keep(err)
	if firstErr != nil {
		return CRIStatus{}, fmt.Errorf("containers: decode CRI container status: %w", firstErr)
	}
	st.LogDriver = LogDriverCRI
	if !strings.HasPrefix(st.LogPath, "/") {
		st.LogPath = "" // relative to the pod's log directory, which the status does not carry
	}
	return st, nil
}

func nsTime(x uint64) time.Time {
	if ns := int64(x); ns > 0 {
		return time.Unix(0, ns).UTC()
	}
	return time.Time{}
}

// criRuntimeName decodes a VersionResponse: runtime_name (2) → "containerd", "cri-o", …
func criRuntimeName(msg []byte, sock string) string {
	var name string
	_ = protoFields(msg, func(num protowire.Number, typ protowire.Type, v []byte, _ uint64) {
		if num == 2 && typ == protowire.BytesType {
			name = strings.ToLower(string(v))
		}
	})
	switch {
	case name == "cri-o" || name == "crio" || (name == "" && strings.Contains(sock, "crio")):
		return "cri-o"
	case name == "":
		return "containerd"
	}
	return name
}

// criSocketError classifies a dial/call error: absent (try the next candidate), permission or other.
func criAbsent(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED)
}

func permissionError(err error) bool {
	return errors.Is(err, fs.ErrPermission) || errors.Is(err, syscall.EACCES) || errors.Is(err, syscall.EPERM)
}

// listCRI lists the containers of every CRI socket that answers. ErrNoRuntime is returned when no
// socket serves the CRI; permission or other errors are returned only when no socket succeeded.
// Called with s.mu held.
func (s *Source) listCRI(ctx context.Context) ([]Container, error) {
	if s.criSkip == nil {
		s.criSkip, s.criRuntime = map[string]time.Time{}, map[string]string{}
	}
	var out []Container
	var lastErr error = ErrNoRuntime
	ok := false
	seen := map[string]bool{}
	now := time.Now()
	s.criLive = map[string]bool{}
	for _, configured := range s.CRISockets {
	candidates:
		for _, sock := range s.socketCandidatesFor(configured) {
			if seen[sock] {
				continue
			}
			seen[sock] = true
			if until, skip := s.criSkip[sock]; skip && now.Before(until) {
				continue
			}
			cs, err := s.listCRISocket(ctx, sock)
			switch {
			case err == nil:
				ok = true
				out = append(out, cs...)
			case criAbsent(err):
				continue
			case errors.Is(err, errCRIUnimplemented):
				s.criSkip[sock] = now.Add(criSkipFor)
			case permissionError(err):
				lastErr = fmt.Errorf("containers: %s: %w", sock, err)
			case !permissionError(lastErr):
				lastErr = fmt.Errorf("containers: %s: %w", sock, err)
			}
			break candidates // the remaining candidates are the same socket
		}
	}
	for id := range s.criDetails {
		if !s.criLive[id] {
			delete(s.criDetails, id)
		}
	}
	if ok {
		return out, nil
	}
	return nil, lastErr
}

func (s *Source) listCRISocket(ctx context.Context, sock string) ([]Container, error) {
	tr := criTransport(sock)
	defer tr.CloseIdleConnections()
	call := func(method string, msg []byte) ([]byte, error) {
		timeout := s.Timeout
		if timeout <= 0 {
			timeout = 5 * time.Second
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return criCall(cctx, tr, method, msg)
	}
	runtime, known := s.criRuntime[sock]
	if !known {
		// VersionRequest{version: "v1"}
		resp, err := call("Version", protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), "v1"))
		if err != nil {
			return nil, err
		}
		runtime = criRuntimeName(resp, sock)
		s.criRuntime[sock] = runtime
	}
	resp, err := call("ListContainers", nil)
	if err != nil {
		return nil, err
	}
	cs, err := ParseCRIList(resp, runtime)
	if err != nil {
		return nil, err
	}
	s.inspectCRI(cs, func(id string) (CRIStatus, error) {
		msg := protowire.AppendString(protowire.AppendTag(nil, 1, protowire.BytesType), id)
		resp, err := call("ContainerStatus", msg)
		if err != nil {
			return CRIStatus{}, err
		}
		return ParseCRIStatus(resp)
	})
	return cs, nil
}

type criDetailEntry struct {
	st    CRIStatus
	state string
	at    time.Time
}

// inspectCRI fills start/finish times, exit code and log path from ContainerStatus, with the same cache
// policy as Docker inspection. Called with s.mu held.
func (s *Source) inspectCRI(cs []Container, status func(id string) (CRIStatus, error)) {
	now := time.Now()
	if s.criDetails == nil {
		s.criDetails = map[string]criDetailEntry{}
	}
	n := 0
	for i := range cs {
		c := &cs[i]
		s.criLive[c.ID] = true
		e, ok := s.criDetails[c.ID]
		stale := !ok || e.state != c.State || (c.State == "running" && now.Sub(e.at) >= inspectRefresh)
		if stale && n < maxInspectPerList {
			n++
			if st, err := status(c.ID); err == nil {
				if st.RestartCount == 0 {
					st.RestartCount = c.RestartCount // from the list annotations
				}
				e, ok = criDetailEntry{st: st, state: c.State, at: now}, true
				s.criDetails[c.ID] = e
			}
		}
		if ok {
			if strings.HasPrefix(c.Image, "sha256:") && e.st.Image != "" {
				c.Image = e.st.Image
			}
			c.Apply(e.st.Details)
		}
	}
}
