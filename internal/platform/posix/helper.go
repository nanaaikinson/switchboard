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
	"sync"
	"syscall"
	"time"
)

// ErrNoHelper means the privileged helper is not installed or not running.
var ErrNoHelper = errors.New("privileged helper not available")

// helperProtocol is bumped on incompatible changes between helper and daemon.
const helperProtocol = 1

const helperIOTimeout = 5 * time.Second

// Request is one line of JSON from the daemon to the helper.
type Request struct {
	Version int      `json:"version"`
	Op      string   `json:"op"`              // "listeners" or "hosts"
	Names   []string `json:"names,omitempty"` // for "hosts"
}

// Response is the helper's one-line JSON answer. For "listeners", the
// sockets travel alongside it as SCM_RIGHTS.
type Response struct {
	Version int      `json:"version"`
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
	// Hosts rewrites the hosts-file entries; nil refuses the "hosts" op.
	Hosts func(names []string) error
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
	var ports portSet
	defer ports.close()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("helper: accept: %w", err)
		}
		s.handle(c.(*net.UnixConn), &ports)
	}
}

func (s *Server) handle(c *net.UnixConn, ports *portSet) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(helperIOTimeout))
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
		files, resp.Addrs, err = ports.files(s.Addrs)
		if err != nil {
			resp.Error = err.Error()
		}
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
	slog.Info("helper answered", "op", req.Op, "sockets", len(fds), "error", resp.Error)
}

// portSet binds the HTTP ports once and keeps them for the helper's lifetime.
type portSet struct {
	mu  sync.Mutex
	lns []*net.TCPListener
}

// files returns fresh duplicates of the listening sockets, binding on first use.
func (s *portSet) files(addrs []string) ([]*os.File, []string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lns == nil {
		for _, a := range addrs {
			ln, err := net.Listen("tcp", a)
			if err != nil {
				s.closeLocked()
				return nil, nil, fmt.Errorf("bind %s: %w; is another web server using it? Run 'sb doctor'", a, err)
			}
			s.lns = append(s.lns, ln.(*net.TCPListener))
		}
	}
	files := make([]*os.File, 0, len(s.lns))
	bound := make([]string, 0, len(s.lns))
	for _, ln := range s.lns {
		f, err := ln.File()
		if err != nil {
			for _, f := range files {
				_ = f.Close()
			}
			return nil, nil, fmt.Errorf("dup listener: %w", err)
		}
		files = append(files, f)
		bound = append(bound, ln.Addr().String())
	}
	return files, bound, nil
}

func (s *portSet) close() { s.mu.Lock(); s.closeLocked(); s.mu.Unlock() }

func (s *portSet) closeLocked() {
	for _, ln := range s.lns {
		_ = ln.Close()
	}
	s.lns = nil
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
	c, err := dial(ctx, socket, Request{Op: "listeners"})
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
		files[i] = os.NewFile(uintptr(fd), "helper-listener")
	}
	defer func() {
		for _, f := range files {
			_ = f.Close() // net.FileListener dups; the originals are ours to close
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
	if resp.Error != "" {
		return nil, fmt.Errorf("helper: %s", resp.Error)
	}
	if len(files) != len(resp.Addrs) || len(files) == 0 {
		return nil, fmt.Errorf("helper sent %d sockets for %d addresses", len(files), len(resp.Addrs))
	}
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
