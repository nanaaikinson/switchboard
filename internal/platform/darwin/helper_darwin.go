package darwin

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type helperRequest struct {
	Version int    `json:"version"`
	Op      string `json:"op"` // "listeners"
}

type helperResponse struct {
	Version int      `json:"version"`
	Addrs   []string `json:"addrs,omitempty"`
	Error   string   `json:"error,omitempty"`
}

// ServeHelper runs the root side of the helper protocol until ctx is done. It
// listens on a socket that only the installing user can connect to, binds the
// HTTP ports on first request, and sends duplicates of the listening sockets
// to each caller. It never accepts connections on them itself.
func (p *Platform) ServeHelper(ctx context.Context) error {
	gid, err := p.userGID()
	if err != nil {
		return err
	}
	sock := p.o.HelperSocket
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
	if err := os.Lchown(sock, p.o.UID, gid); err != nil {
		return fmt.Errorf("helper: chown socket: %w", err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return fmt.Errorf("helper: chmod socket: %w", err)
	}
	slog.Info("helper listening", "socket", sock, "uid", p.o.UID)

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
		p.handleHelperConn(c.(*net.UnixConn), &ports)
	}
}

func (p *Platform) handleHelperConn(c *net.UnixConn, ports *portSet) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(helperIOTimeout))
	var req helperRequest
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err == nil {
		err = json.Unmarshal(line, &req)
	}
	resp := helperResponse{Version: helperProtocol}
	var files []*os.File
	switch {
	case err != nil:
		resp.Error = "bad request: " + err.Error()
	case req.Version != helperProtocol:
		resp.Error = fmt.Sprintf("protocol version %d, helper speaks %d; re-run 'sb setup'", req.Version, helperProtocol)
	case req.Op != "listeners":
		resp.Error = fmt.Sprintf("unknown op %q", req.Op)
	default:
		files, resp.Addrs, err = ports.files(p.o.HelperAddrs)
		if err != nil {
			resp.Error = err.Error()
		}
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
	slog.Info("helper sent listeners", "count", len(fds), "error", resp.Error)
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

// HelperListeners asks the helper for the HTTP listening sockets. It returns
// ErrNoHelper if the helper socket is missing or refuses the connection.
func (p *Platform) HelperListeners(ctx context.Context) ([]net.Listener, error) {
	d := net.Dialer{Timeout: 2 * time.Second}
	conn, err := d.DialContext(ctx, "unix", p.o.HelperSocket)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoHelper, err)
	}
	c := conn.(*net.UnixConn)
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(helperIOTimeout))

	req, _ := json.Marshal(helperRequest{Version: helperProtocol, Op: "listeners"})
	if _, err := c.Write(append(req, '\n')); err != nil {
		return nil, fmt.Errorf("helper: send request: %w", err)
	}

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
	var resp helperResponse
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
