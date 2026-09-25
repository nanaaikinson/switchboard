package darwin

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/nanaaikinson/switchboard/internal/mdns"
)

// mDNSResponder's client protocol, from dnssd_ipc.h in Apple's open-source
// mDNSResponder. It is spoken directly, as libdns_sd does, so sb needs no
// cgo. Records are registered on one connection with
// DNSServiceRegisterRecord semantics and die with it.
const (
	dnssdSocket = "/var/run/mDNSResponder"

	dnssdVersion     = 1
	dnssdHeaderLen   = 28
	opConnection     = 1  // connection_request
	opRegisterRecord = 2  // reg_record_request
	opRemoveRecord   = 3  // remove_record_request
	opRegRecordReply = 69 // reg_record_reply_op
	opAsyncError     = 73 // async_error_op

	flagShared  = 0x10       // kDNSServiceFlagsShared: no probing, no conflicts
	ifLocalOnly = 0xFFFFFFFF // kDNSServiceInterfaceIndexLocalOnly: this machine only
	dnssdClass  = 1          // IN

	dnssdTimeout = 5 * time.Second
)

// MDNS announces .local names through mDNSResponder, as records only
// processes on this Mac can see.
func (p *Platform) MDNS() (mdns.Backend, error) {
	return mdns.Backend{Name: "mDNSResponder", Interface: "loopback", Open: func() (mdns.Publisher, error) {
		return openDNSSD(p.o.DNSSDSocket)
	}}, nil
}

// dnssd is one connection to mDNSResponder.
type dnssd struct {
	conn *net.UnixConn

	mu   sync.Mutex // serializes requests
	next uint32     // next record index
	regs map[string][]uint32

	lost     chan struct{} // closed when the connection drops
	lostOnce sync.Once
}

func openDNSSD(path string) (*dnssd, error) {
	if path == "" {
		path = dnssdSocket
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("connect to mDNSResponder at %s: %w", path, err)
	}
	d := &dnssd{conn: c, regs: map[string][]uint32{}, lost: make(chan struct{})}
	if err := d.request(opConnection, 0, 0, nil, false); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("mDNSResponder: open connection: %w", err)
	}
	go d.read()
	return d, nil
}

// Set implements mdns.Publisher.
func (d *dnssd) Set(names []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	select {
	case <-d.lost:
		return errors.New("connection to mDNSResponder lost")
	default:
	}
	for name, idxs := range d.regs {
		if slices.Contains(names, name) {
			continue
		}
		for _, idx := range idxs {
			if err := d.request(opRemoveRecord, 0, idx, be32(0), true); err != nil {
				return fmt.Errorf("withdraw %s: %w", name, err)
			}
		}
		delete(d.regs, name)
	}
	for _, name := range names {
		if _, ok := d.regs[name]; ok {
			continue
		}
		var idxs []uint32
		for _, rr := range []struct {
			typ  uint16
			data []byte
		}{{1, net.IPv4(127, 0, 0, 1).To4()}, {28, net.IPv6loopback}} { // A, AAAA
			idx := d.next
			d.next++
			if err := d.request(opRegisterRecord, idx+1, idx, registerBody(name, rr.typ, rr.data), true); err != nil {
				for _, done := range idxs {
					_ = d.request(opRemoveRecord, 0, done, be32(0), true)
				}
				return fmt.Errorf("announce %s: %w", name, err)
			}
			idxs = append(idxs, idx)
		}
		d.regs[name] = idxs
	}
	return nil
}

// Close implements mdns.Publisher. mDNSResponder withdraws every record of
// a connection, with goodbyes, when it closes.
func (d *dnssd) Close() error { return d.conn.Close() }

// Lost is closed when mDNSResponder drops the connection, as when it
// restarts.
func (d *dnssd) Lost() <-chan struct{} { return d.lost }

// registerBody is a reg_record_request after the return-socket byte.
func registerBody(name string, rrtype uint16, rdata []byte) []byte {
	b := binary.BigEndian.AppendUint32(nil, flagShared)
	b = binary.BigEndian.AppendUint32(b, ifLocalOnly)
	b = append(b, name+"."...)
	b = append(b, 0)
	b = binary.BigEndian.AppendUint16(b, rrtype)
	b = binary.BigEndian.AppendUint16(b, dnssdClass)
	b = binary.BigEndian.AppendUint16(b, uint16(len(rdata))) //nolint:gosec // G115: an IPv4 or IPv6 address
	b = append(b, rdata...)
	return binary.BigEndian.AppendUint32(b, mdns.TTL)
}

// request sends one message and returns mDNSResponder's result. With
// separate, the body is preceded by an empty return-socket path, and the
// result comes back on a socket pair whose other end is passed with the
// message's last byte, as libdns_sd does for record operations. Otherwise
// it comes back on the connection. ctx is echoed in async replies.
func (d *dnssd) request(op, ctx, index uint32, body []byte, separate bool) error {
	data := body
	if separate {
		data = append([]byte{0}, body...)
	}
	msg := make([]byte, dnssdHeaderLen, dnssdHeaderLen+len(data))
	binary.BigEndian.PutUint32(msg[0:], dnssdVersion)
	binary.BigEndian.PutUint32(msg[4:], uint32(len(data))) //nolint:gosec // G115: one record, well under 4 GiB
	binary.BigEndian.PutUint32(msg[12:], op)
	binary.BigEndian.PutUint32(msg[16:], ctx) // client_context is opaque; 20:24 stays 0
	binary.BigEndian.PutUint32(msg[24:], index)
	msg = append(msg, data...)

	_ = d.conn.SetWriteDeadline(time.Now().Add(dnssdTimeout))
	defer func() { _ = d.conn.SetWriteDeadline(time.Time{}) }()
	if !separate {
		if _, err := d.conn.Write(msg); err != nil {
			return err
		}
		_ = d.conn.SetReadDeadline(time.Now().Add(dnssdTimeout))
		defer func() { _ = d.conn.SetReadDeadline(time.Time{}) }()
		return readResult(d.conn)
	}

	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("socketpair: %w", err)
	}
	mine := os.NewFile(uintptr(fds[0]), "dnssd-result")
	defer mine.Close()
	errc, err := net.FileConn(mine)
	if err != nil {
		_ = syscall.Close(fds[1])
		return fmt.Errorf("result socket: %w", err)
	}
	defer errc.Close()

	last := len(msg) - 1
	_, err = d.conn.Write(msg[:last])
	if err == nil {
		_, _, err = d.conn.WriteMsgUnix(msg[last:], syscall.UnixRights(fds[1]), nil)
	}
	_ = syscall.Close(fds[1]) // mDNSResponder has its own copy now
	if err != nil {
		return err
	}
	_ = errc.SetReadDeadline(time.Now().Add(dnssdTimeout))
	return readResult(errc)
}

// readResult reads a 4-byte DNSServiceErrorType.
func readResult(r io.Reader) error {
	var b [4]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return fmt.Errorf("read result: %w", err)
	}
	if code := int32(binary.BigEndian.Uint32(b[:])); code != 0 { //nolint:gosec // G115: DNSServiceErrorType is signed on the wire
		return dnssdError(code)
	}
	return nil
}

// read drains replies until the connection closes. Record replies only
// carry an error for records that failed after being accepted.
func (d *dnssd) read() {
	defer d.lostOnce.Do(func() { close(d.lost) })
	hdr := make([]byte, dnssdHeaderLen)
	for {
		if _, err := io.ReadFull(d.conn, hdr); err != nil {
			if !errors.Is(err, net.ErrClosed) {
				slog.Debug("mdns: mDNSResponder connection closed", "err", err)
			}
			return
		}
		body := make([]byte, binary.BigEndian.Uint32(hdr[4:]))
		if _, err := io.ReadFull(d.conn, body); err != nil {
			return
		}
		op := binary.BigEndian.Uint32(hdr[12:])
		if (op == opRegRecordReply || op == opAsyncError) && len(body) >= 12 {
			if code := int32(binary.BigEndian.Uint32(body[8:])); code != 0 { //nolint:gosec // G115: signed on the wire
				slog.Warn("mdns: mDNSResponder dropped a record", "err", dnssdError(code))
			}
		}
	}
}

func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }

// dnssdError is a DNSServiceErrorType from dns_sd.h.
type dnssdError int32

func (e dnssdError) Error() string {
	switch e {
	case -65540:
		return "bad parameter (kDNSServiceErr_BadParam)"
	case -65548:
		return "name conflict (kDNSServiceErr_NameConflict)"
	case -65563:
		return "mDNSResponder is not running (kDNSServiceErr_ServiceNotRunning)"
	case -65570:
		return "denied by policy (kDNSServiceErr_PolicyDenied)"
	}
	return fmt.Sprintf("mDNSResponder error %d", int32(e))
}
