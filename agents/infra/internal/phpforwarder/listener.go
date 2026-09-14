package phpforwarder

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
)

// DefaultSocketGroup is the socket group when php_forwarder.socket_group is empty or "auto" (php-agent.md §1, D-103).
// The privileged reconcile step creates it, adds the agent user and the PHP-FPM pool users to it.
const DefaultSocketGroup = "openlog-php"

// AutoSocketGroups are the fallback, tried in order when DefaultSocketGroup does not exist (reconcile never ran:
// container, dev build, legacy unit).
var AutoSocketGroups = []string{"www-data", "nginx", "apache", "php-fpm"}

// groupWarning is a socket group that could not be applied. fallback marks the pre-openlog-php best effort (the
// socket still works for the agent's own group and 0666): logged at info level once, not counted as a permission gap.
type groupWarning struct {
	fallback bool
	err      error
}

func (w *groupWarning) Error() string { return w.err.Error() }
func (w *groupWarning) Unwrap() error { return w.err }

// readBufferBytes is the requested kernel receive buffer of the sockets.
const readBufferBytes = 4 << 20

// socketInfo describes the bound unix socket.
type socketInfo struct {
	path  string
	stat  os.FileInfo // to remove only our own socket on stop
	group string      // group applied to the socket, "" if none
}

// listenUnix creates the socket directory (0755) if needed, replaces a stale socket, binds and applies mode and
// group. A group that cannot be applied is returned as warn; the socket stays usable for the agent's own group.
func listenUnix(path, group string, mode fs.FileMode, lookup func(string) (*user.Group, error)) (conn *net.UnixConn, info socketInfo, warn, err error) {
	info.path = path
	dir := filepath.Dir(path)
	if _, serr := os.Stat(dir); errors.Is(serr, fs.ErrNotExist) {
		if err = os.MkdirAll(dir, 0o755); err != nil {
			return nil, info, nil, fmt.Errorf("socket directory: %w", err)
		}
		if err = os.Chmod(dir, 0o755); err != nil {
			return nil, info, nil, fmt.Errorf("socket directory: %w", err)
		}
	} else if serr != nil {
		return nil, info, nil, fmt.Errorf("socket directory: %w", serr)
	}
	if fi, lerr := os.Lstat(path); lerr == nil {
		if fi.Mode().Type() != fs.ModeSocket {
			return nil, info, nil, fmt.Errorf("%s exists and is not a socket", path)
		}
		// A stale socket of a previous (killed or restarted) agent: senders use unconnected sendto and pick up
		// the new inode automatically.
		if err = os.Remove(path); err != nil {
			return nil, info, nil, fmt.Errorf("remove stale socket: %w", err)
		}
	}
	conn, err = net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, info, nil, err
	}
	fail := func(e error) (*net.UnixConn, socketInfo, error, error) {
		_ = conn.Close()
		_ = os.Remove(path)
		return nil, info, nil, e
	}
	if err = os.Chmod(path, mode); err != nil {
		return fail(fmt.Errorf("chmod socket: %w", err))
	}
	if info.stat, err = os.Lstat(path); err != nil {
		return fail(err)
	}
	_ = conn.SetReadBuffer(readBufferBytes)

	name, gid, fallback, gerr := resolveGroup(group, lookup)
	switch {
	case gerr != nil:
		warn = &groupWarning{err: gerr}
	case gid >= 0:
		cerr := os.Chown(path, -1, gid)
		switch {
		case cerr == nil:
			info.group = name
		case fallback:
			warn = &groupWarning{fallback: true, err: fmt.Errorf("group %s does not exist (the privileged step "+
				"`openlog-infra-agent -reconcile` creates it; the systemd unit runs it on every start: systemctl restart "+
				"openlog-infra-agent); the fallback group %s was not applied either because the agent user is not a member; "+
				"PHP workers of other users cannot send: %w", DefaultSocketGroup, name, cerr)}
		case name == DefaultSocketGroup:
			warn = &groupWarning{err: fmt.Errorf("socket group %s not applied: the agent user is not a member yet "+
				"(systemctl restart openlog-infra-agent lets the privileged pre-start step add it): %w", name, cerr)}
		default:
			warn = &groupWarning{err: fmt.Errorf("socket group %s not applied (the agent user must be a member of the group, e.g. "+
				"SupplementaryGroups=%s in a systemd drop-in): %w", name, name, cerr)}
		}
	}
	return conn, info, warn, nil
}

// resolveGroup returns the configured group, DefaultSocketGroup, or the first existing auto group (fallback). gid is -1
// when none applies.
func resolveGroup(group string, lookup func(string) (*user.Group, error)) (name string, gid int, fallback bool, err error) {
	if lookup == nil {
		lookup = user.LookupGroup
	}
	byName := func(name string) (int, error) {
		if gid, err := strconv.Atoi(name); err == nil {
			return gid, nil
		}
		g, err := lookup(name)
		if err != nil {
			return -1, err
		}
		return strconv.Atoi(g.Gid)
	}
	if group != "" && group != "auto" {
		gid, err := byName(group)
		if err != nil {
			return group, -1, false, fmt.Errorf("socket group %q: %w", group, err)
		}
		return group, gid, false, nil
	}
	if gid, err := byName(DefaultSocketGroup); err == nil {
		return DefaultSocketGroup, gid, false, nil
	}
	for _, name := range AutoSocketGroups {
		if gid, err := byName(name); err == nil {
			return name, gid, true, nil
		}
	}
	return "", -1, false, fmt.Errorf("neither %s nor any of the fallback groups %v exists; PHP workers of other users cannot send "+
		"(systemctl restart openlog-infra-agent creates %s, or set php_forwarder.socket_group or socket_mode: \"0666\")",
		DefaultSocketGroup, AutoSocketGroups, DefaultSocketGroup)
}

// removeSocket deletes the socket file if it is still the one this listener created.
func removeSocket(info socketInfo) {
	if info.stat == nil {
		return
	}
	if fi, err := os.Lstat(info.path); err == nil && os.SameFile(fi, info.stat) {
		_ = os.Remove(info.path)
	}
}

func listenUDP(addr string) (*net.UDPConn, error) {
	ua, err := net.ResolveUDPAddr("udp", addr)
	if err != nil {
		return nil, err
	}
	conn, err := net.ListenUDP("udp", ua)
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadBuffer(readBufferBytes)
	return conn, nil
}
