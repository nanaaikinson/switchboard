//go:build darwin || linux

package posix

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ErrNoHelper means the privileged helper is not installed or not running.
var ErrNoHelper = errors.New("privileged helper not available")

// ErrOldHelper means the helper is from an earlier version that lacks an op.
var ErrOldHelper = errors.New("privileged helper is out of date")

// helperProtocol is bumped on incompatible changes between helper and daemon.
const helperProtocol = 1

const helperIOTimeout = 5 * time.Second

// Request is one line of JSON from the daemon to the helper.
type Request struct {
	Version int      `json:"version"`
	Op      string   `json:"op"`              // "listeners", "dns", "hosts" or "version"
	Names   []string `json:"names,omitempty"` // for "hosts"
}

// Response is the helper's one-line JSON answer. For "listeners" and "dns",
// the sockets travel alongside it as SCM_RIGHTS.
type Response struct {
	Version int      `json:"version"`
	Build   string   `json:"build,omitempty"` // for "version": the helper's sb version
	Addrs   []string `json:"addrs,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// maxRequest bounds a request line.
const maxRequest = 1 << 20

// Server is the root side of the helper protocol.
type Server struct {
	Socket   string   // Unix socket path
	UID, GID int      // the only user allowed to connect
	Addrs    []string // TCP addresses to bind and hand out
	// DNSAddr is the loopback address the helper binds for the daemon's DNS
	// server, UDP and TCP, on a privileged port, so no other user can bind
	// it first and answer lookups. "" refuses the "dns" op.
	DNSAddr string
	// Hosts rewrites the hosts-file entries; nil refuses the "hosts" op.
	Hosts func(names []string) error
	// Build is the helper's sb version, for the "version" op.
	Build string
}

// Serve runs until ctx is done. It listens on a socket that only the user can
// connect to, binds the ports on first request, and sends duplicates of the
// listening sockets to each caller. It never accepts connections on them.
func (s *Server) Serve(ctx context.Context) error {
	sock := s.Socket
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil { //nolint:gosec // G301: root-owned; the user must traverse it to reach the 0600 socket
		return fmt.Errorf("helper: %w", err)
	}
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("helper: remove stale socket: %w", err)
	}
	// Created root-owned; only after chown can the user connect.
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("helper: listen %s: %w", sock, err)
	}
	defer ln.Close()
	// chmod while root still owns it: after the chown, changing the mode
	// needs CAP_FOWNER, which the Linux unit does not grant.
	if err := os.Chmod(sock, 0o600); err != nil {
		return fmt.Errorf("helper: chmod socket: %w", err)
	}
	if err := os.Lchown(sock, s.UID, s.GID); err != nil {
		return fmt.Errorf("helper: chown socket: %w", err)
	}
	slog.Info("helper listening", "socket", sock, "uid", s.UID)

	go func() { <-ctx.Done(); _ = ln.Close() }()
	var ports, dns socketSet
	defer ports.close()
	defer dns.close()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("helper: accept: %w", err)
		}
		s.handle(c.(*net.UnixConn), &ports, &dns)
	}
}

func (s *Server) handle(c *net.UnixConn, ports, dns *socketSet) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(helperIOTimeout))
	// The socket's mode and owner already keep others out; checking the
	// peer's uid as well means a mistake there can't hand them the ports.
	if uid, err := peerUID(c); err != nil || uid != s.UID && uid != 0 {
		slog.Warn("helper: refused a connection from another user", "uid", uid, "err", err)
		return
	}
	var req Request
	line, err := bufio.NewReader(io.LimitReader(c, maxRequest)).ReadBytes('\n')
	if err == nil {
		err = json.Unmarshal(line, &req)
	}
	resp := Response{Version: helperProtocol}
	var files []*os.File
	switch {
	case err != nil:
		resp.Error = "bad request: " + err.Error()
	case req.Version != helperProtocol:
		resp.Error = fmt.Sprintf("protocol version %d, helper speaks %d; re-run 'sb setup'", req.Version, helperProtocol)
	case req.Op == "listeners":
		files, resp.Addrs, err = ports.files(func() ([]boundSocket, error) { return bindHTTP(s.Addrs) })
		if err != nil {
			resp.Error = err.Error()
		}
	case req.Op == "dns" && s.DNSAddr != "":
		files, resp.Addrs, err = dns.files(func() ([]boundSocket, error) { return bindDNS(s.DNSAddr) })
		if err != nil {
			resp.Error = err.Error()
		}
	case req.Op == "version":
		resp.Build = s.Build
	case req.Op == "hosts" && s.Hosts != nil:
		if err := s.Hosts(req.Names); err != nil {
			resp.Error = err.Error()
		}
	default:
		resp.Error = fmt.Sprintf("unknown op %q", req.Op)
	}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	data, _ := json.Marshal(resp)
	fds := make([]int, len(files))
	for i, f := range files {
		fds[i] = int(f.Fd())
	}
	var oob []byte
	if len(fds) > 0 {
		oob = syscall.UnixRights(fds...)
	}
	if _, _, err := c.WriteMsgUnix(append(data, '\n'), oob, nil); err != nil {
		slog.Warn("helper: send listeners", "err", err)
		return
	}
	// Errors can name hostnames (from the hosts op), so only at debug level.
	slog.Info("helper answered", "op", req.Op, "sockets", len(fds), "failed", resp.Error != "")
	if resp.Error != "" {
		slog.Debug("helper error", "op", req.Op, "error", resp.Error)
	}
}

// boundSocket is a listening TCP or UDP socket the helper hands out.
type boundSocket interface {
	File() (*os.File, error)
	Close() error
}

func sockAddr(b boundSocket) string {
	switch b := b.(type) {
	case net.Listener:
		return b.Addr().String()
	case net.PacketConn:
		return b.LocalAddr().String()
	}
	return ""
}

// socketSet binds a set of sockets once and keeps them for the helper's
// lifetime.
type socketSet struct {
	mu    sync.Mutex
	socks []boundSocket
}

// files returns fresh duplicates of the sockets, binding them on first use.
func (s *socketSet) files(bind func() ([]boundSocket, error)) ([]*os.File, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.socks == nil {
		socks, err := bind()
		if err != nil {
			return nil, nil, err
		}
		s.socks = socks
	}
	files := make([]*os.File, 0, len(s.socks))
	bound := make([]string, 0, len(s.socks))
	for _, b := range s.socks {
		f, err := b.File()
		if err != nil {
			for _, f := range files {
				_ = f.Close()
			}
			return nil, nil, fmt.Errorf("dup socket: %w", err)
		}
		files = append(files, f)
		bound = append(bound, sockAddr(b))
	}
	return files, bound, nil
}

func (s *socketSet) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	closeAll(s.socks)
	s.socks = nil
}

func closeAll(socks []boundSocket) {
	for _, b := range socks {
		_ = b.Close()
	}
}

// bindHTTP binds the proxy's TCP ports.
func bindHTTP(addrs []string) ([]boundSocket, error) {
	var socks []boundSocket
	for _, a := range addrs {
		ln, err := net.Listen("tcp", a)
		if err != nil {
			closeAll(socks)
			return nil, fmt.Errorf("bind %s: %w; is another web server using it? Run 'sb doctor'", a, err)
		}
		socks = append(socks, ln.(*net.TCPListener))
	}
	return socks, nil
}

// bindDNS binds addr for UDP, then TCP. On macOS, where anyone may bind a
// privileged port on the wildcard address, SO_REUSEADDR lets the helper bind
// the loopback address anyway if someone did; the more specific socket gets
// the traffic.
func bindDNS(addr string) ([]boundSocket, error) {
	host, _, err := net.SplitHostPort(addr)
	if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
		return nil, fmt.Errorf("dns address %q is not a loopback IP and port", addr)
	}
	lc := net.ListenConfig{Control: reuseAddr}
	pc, err := lc.ListenPacket(context.Background(), "udp", addr)
	if err != nil {
		return nil, fmt.Errorf("bind udp %s: %w; is another DNS server using it? Run 'sb doctor'", addr, err)
	}
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("bind tcp %s: %w; is another DNS server using it? Run 'sb doctor'", addr, err)
	}
	return []boundSocket{pc.(*net.UDPConn), ln.(*net.TCPListener)}, nil
}

func reuseAddr(_, _ string, c syscall.RawConn) error {
	if runtime.GOOS != "darwin" {
		return nil
	}
	var serr error
	if err := c.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	}); err != nil {
		return err
	}
	return serr
}

// dial connects to the helper and sends req.
func dial(ctx context.Context, socket string, req Request) (*net.UnixConn, error) {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoHelper, err)
	}
	c := conn.(*net.UnixConn)
	_ = c.SetDeadline(time.Now().Add(helperIOTimeout))
	req.Version = helperProtocol
	data, _ := json.Marshal(req)
	if _, err := c.Write(append(data, '\n')); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("helper: send request: %w", err)
	}
	return c, nil
}

// HelperBuild asks the helper which version of sb it runs. It returns an
// error matching ErrOldHelper for a helper from before it could say.
func HelperBuild(ctx context.Context, socket string) (string, error) {
	c, err := dial(ctx, socket, Request{Op: "version"})
	if err != nil {
		return "", err
	}
	defer c.Close()
	var resp Response
	if err := json.NewDecoder(io.LimitReader(c, maxRequest)).Decode(&resp); err != nil {
		return "", fmt.Errorf("helper: decode response: %w", err)
	}
	switch {
	case strings.HasPrefix(resp.Error, "unknown op "):
		return "", ErrOldHelper
	case resp.Error != "":
		return "", fmt.Errorf("helper: %s", resp.Error)
	}
	return resp.Build, nil
}

// CheckHelperBuild reports, with a fix, if the helper doesn't run sb want.
// The root helper runs its own copy of sb, which only 'sb setup' updates, so
// after an upgrade it can miss fixes to the privileged part.
func CheckHelperBuild(ctx context.Context, socket, want string) error {
	build, err := HelperBuild(ctx, socket)
	switch {
	case errors.Is(err, ErrOldHelper):
		return WithFix("Re-run 'sb setup' to update it.", "running, but from an earlier version of sb than %s", want)
	case err != nil:
		return err
	case build != want:
		return WithFix("Re-run 'sb setup' to update it.", "running sb %s, but this is sb %s", build, want)
	}
	return nil
}

// SyncHosts asks the helper to set the hosts-file entries to names.
func SyncHosts(ctx context.Context, socket string, names []string) error {
	c, err := dial(ctx, socket, Request{Op: "hosts", Names: names})
	if err != nil {
		return err
	}
	defer c.Close()
	var resp Response
	if err := json.NewDecoder(io.LimitReader(c, maxRequest)).Decode(&resp); err != nil {
		return fmt.Errorf("helper: decode response: %w", err)
	}
	if resp.Error != "" {
		return fmt.Errorf("helper: %s", resp.Error)
	}
	return nil
}

// Listeners asks the helper for the listening sockets. It returns
// ErrNoHelper if the helper socket is missing or refuses the connection.
func Listeners(ctx context.Context, socket string) ([]net.Listener, error) {
	files, err := receive(ctx, socket, "listeners")
	if err != nil {
		return nil, err
	}
	defer closeFiles(files) // net.FileListener dups; the originals are ours to close
	lns := make([]net.Listener, 0, len(files))
	for _, f := range files {
		ln, err := net.FileListener(f)
		if err != nil {
			for _, l := range lns {
				_ = l.Close()
			}
			return nil, fmt.Errorf("helper: use received socket: %w", err)
		}
		lns = append(lns, ln)
	}
	return lns, nil
}

// DNSSockets asks the helper for the DNS server's UDP and TCP sockets. It
// returns ErrNoHelper if there is no helper, and an error saying to re-run
// 'sb setup' if the helper is from a version that doesn't bind DNS.
func DNSSockets(ctx context.Context, socket string) (net.PacketConn, net.Listener, error) {
	files, err := receive(ctx, socket, "dns")
	if err != nil {
		return nil, nil, err
	}
	defer closeFiles(files)
	if len(files) != 2 {
		return nil, nil, fmt.Errorf("helper sent %d DNS sockets, want 2", len(files))
	}
	pc, err := net.FilePacketConn(files[0])
	if err != nil {
		return nil, nil, fmt.Errorf("helper: use received UDP socket: %w", err)
	}
	ln, err := net.FileListener(files[1])
	if err != nil {
		_ = pc.Close()
		return nil, nil, fmt.Errorf("helper: use received TCP socket: %w", err)
	}
	return pc, ln, nil
}

// receive sends op and returns the sockets that came with the answer.
func receive(ctx context.Context, socket, op string) ([]*os.File, error) {
	c, err := dial(ctx, socket, Request{Op: op})
	if err != nil {
		return nil, err
	}
	defer c.Close()

	buf := make([]byte, 4096)
	oob := make([]byte, syscall.CmsgSpace(16*4))
	n, oobn, _, _, err := c.ReadMsgUnix(buf, oob)
	if err != nil {
		return nil, fmt.Errorf("helper: read response: %w", err)
	}
	fds, err := parseRights(oob[:oobn])
	if err != nil {
		return nil, err
	}
	files := make([]*os.File, len(fds))
	for i, fd := range fds {
		files[i] = os.NewFile(uintptr(fd), "helper-socket")
	}
	ok := false
	defer func() {
		if !ok {
			closeFiles(files)
		}
	}()

	data := buf[:n]
	for !bytes.Contains(data, []byte{'\n'}) && len(data) < 64<<10 {
		m, err := c.Read(buf)
		if err != nil {
			return nil, fmt.Errorf("helper: read response: %w", err)
		}
		data = append(data, buf[:m]...)
	}
	var resp Response
	if err := json.Unmarshal(bytes.TrimSpace(data), &resp); err != nil {
		return nil, fmt.Errorf("helper: decode response: %w", err)
	}
	if resp.Version != helperProtocol {
		return nil, fmt.Errorf("helper speaks protocol %d, daemon %d; re-run 'sb setup'", resp.Version, helperProtocol)
	}
	if strings.HasPrefix(resp.Error, "unknown op ") {
		return nil, fmt.Errorf("%w: the helper is from an earlier version; re-run 'sb setup'", ErrOldHelper)
	}
	if resp.Error != "" {
		return nil, fmt.Errorf("helper: %s", resp.Error)
	}
	if len(files) != len(resp.Addrs) || len(files) == 0 {
		return nil, fmt.Errorf("helper sent %d sockets for %d addresses", len(files), len(resp.Addrs))
	}
	ok = true
	return files, nil
}

func closeFiles(files []*os.File) {
	for _, f := range files {
		_ = f.Close()
	}
}

func parseRights(oob []byte) ([]int, error) {
	if len(oob) == 0 {
		return nil, nil
	}
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, fmt.Errorf("helper: parse control message: %w", err)
	}
	var fds []int
	for i := range msgs {
		rights, err := syscall.ParseUnixRights(&msgs[i])
		if err != nil {
			return nil, fmt.Errorf("helper: parse rights: %w", err)
		}
		fds = append(fds, rights...)
	}
	return fds, nil
}
