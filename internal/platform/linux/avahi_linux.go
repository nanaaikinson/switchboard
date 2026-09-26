package linux

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"slices"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/nanaaikinson/switchboard/internal/mdns"
)

// Avahi's D-Bus API (avahi-daemon/org.freedesktop.Avahi.*.xml).
const (
	avahiName        = "org.freedesktop.Avahi"
	avahiServer      = avahiName + ".Server"
	avahiEntryGroup  = avahiName + ".EntryGroup"
	avahiProtoInet   = 0
	avahiProtoInet6  = 1
	avahiNoReverse   = 1 << 4 // AVAHI_PUBLISH_NO_REVERSE: 127.0.0.1 has many names
	avahiGroupFailed = 4      // AVAHI_ENTRY_GROUP_FAILURE
)

// MDNS announces .local names through avahi-daemon, pinned to the loopback
// interface.
func (p *Platform) MDNS() (mdns.Backend, error) {
	return mdns.Backend{Name: "Avahi", Interface: "loopback", Open: func() (mdns.Publisher, error) {
		lo, err := loopbackIndex()
		if err != nil {
			return nil, err
		}
		bus, err := dialAvahi()
		if err != nil {
			return nil, err
		}
		return newAvahi(bus, lo), nil
	}}, nil
}

func loopbackIndex() (int32, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return 0, fmt.Errorf("list network interfaces: %w", err)
	}
	for _, ifi := range ifs {
		if ifi.Flags&net.FlagLoopback != 0 {
			return int32(ifi.Index), nil //nolint:gosec // G115: interface indexes are small
		}
	}
	return 0, errors.New("no loopback interface")
}

// avahiBus is the part of Avahi's API sb uses.
type avahiBus interface {
	EntryGroupNew() (dbus.ObjectPath, error)
	AddAddress(group dbus.ObjectPath, iface, proto int32, flags uint32, name, addr string) error
	Commit(group dbus.ObjectPath) error
	Free(group dbus.ObjectPath) error
	// Lost is closed when avahi-daemon goes away or a group fails.
	Lost() <-chan struct{}
	Close() error
}

// avahi publishes one entry group per name, so adding or removing a name
// never touches the others.
type avahi struct {
	bus avahiBus
	lo  int32

	mu     sync.Mutex
	groups map[string]dbus.ObjectPath
}

func newAvahi(bus avahiBus, lo int32) *avahi {
	return &avahi{bus: bus, lo: lo, groups: map[string]dbus.ObjectPath{}}
}

// Set implements mdns.Publisher.
func (a *avahi) Set(names []string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, g := range a.groups {
		if !slices.Contains(names, name) {
			if err := a.bus.Free(g); err != nil {
				return fmt.Errorf("withdraw %s: %w", name, err)
			}
			delete(a.groups, name)
		}
	}
	for _, name := range names {
		if _, ok := a.groups[name]; ok {
			continue
		}
		g, err := a.publish(name)
		if err != nil {
			return fmt.Errorf("announce %s: %w", name, err)
		}
		a.groups[name] = g
	}
	return nil
}

func (a *avahi) publish(name string) (dbus.ObjectPath, error) {
	g, err := a.bus.EntryGroupNew()
	if err != nil {
		return "", err
	}
	err = a.bus.AddAddress(g, a.lo, avahiProtoInet, avahiNoReverse, name, "127.0.0.1")
	if err == nil {
		err = a.bus.AddAddress(g, a.lo, avahiProtoInet6, avahiNoReverse, name, "::1")
	}
	if err == nil {
		err = a.bus.Commit(g)
	}
	if err != nil {
		_ = a.bus.Free(g)
		return "", err
	}
	return g, nil
}

// Close implements mdns.Publisher. Avahi also frees a client's groups when
// its connection closes.
func (a *avahi) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	for name, g := range a.groups {
		_ = a.bus.Free(g)
		delete(a.groups, name)
	}
	return a.bus.Close()
}

// Lost implements the announcer's loss notification.
func (a *avahi) Lost() <-chan struct{} { return a.bus.Lost() }

// dbusAvahi is avahiBus over the system bus.
type dbusAvahi struct {
	conn     *dbus.Conn
	lost     chan struct{}
	lostOnce sync.Once

	mu     sync.Mutex
	groups map[dbus.ObjectPath]bool // ours; other clients' signals are ignored
}

func dialAvahi() (*dbusAvahi, error) {
	conn, err := dbus.ConnectSystemBus()
	if err != nil {
		return nil, fmt.Errorf("connect to the D-Bus system bus: %w", err)
	}
	var version string
	if err := conn.Object(avahiName, "/").Call(avahiServer+".GetVersionString", 0).Store(&version); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("avahi-daemon is not reachable (%w); install it and run 'sudo systemctl enable --now avahi-daemon'", err)
	}
	d := &dbusAvahi{conn: conn, lost: make(chan struct{}), groups: map[dbus.ObjectPath]bool{}}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	for _, m := range [][]dbus.MatchOption{
		{dbus.WithMatchSender("org.freedesktop.DBus"), dbus.WithMatchMember("NameOwnerChanged"), dbus.WithMatchArg(0, avahiName)},
		{dbus.WithMatchInterface(avahiEntryGroup), dbus.WithMatchMember("StateChanged")},
	} {
		if err := conn.AddMatchSignal(m...); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("watch avahi-daemon: %w", err)
		}
	}
	go d.watch(signals)
	slog.Debug("mdns: connected to avahi-daemon", "version", version)
	return d, nil
}

// watch closes lost when Avahi restarts or a group fails. The channel is
// closed by godbus when the connection ends.
func (d *dbusAvahi) watch(signals <-chan *dbus.Signal) {
	defer d.lostOnce.Do(func() { close(d.lost) })
	for s := range signals {
		switch s.Name {
		case "org.freedesktop.DBus.NameOwnerChanged":
			return // avahi-daemon stopped or restarted: its groups are gone
		case avahiEntryGroup + ".StateChanged":
			d.mu.Lock()
			ours := d.groups[s.Path]
			d.mu.Unlock()
			if ours && len(s.Body) >= 2 {
				if state, _ := s.Body[0].(int32); state == avahiGroupFailed {
					slog.Warn("mdns: Avahi failed to announce a name", "err", s.Body[1])
					return
				}
			}
		}
	}
}

func (d *dbusAvahi) EntryGroupNew() (dbus.ObjectPath, error) {
	var g dbus.ObjectPath
	if err := d.conn.Object(avahiName, "/").Call(avahiServer+".EntryGroupNew", 0).Store(&g); err != nil {
		return "", err
	}
	d.mu.Lock()
	d.groups[g] = true
	d.mu.Unlock()
	return g, nil
}

func (d *dbusAvahi) AddAddress(g dbus.ObjectPath, iface, proto int32, flags uint32, name, addr string) error {
	return d.conn.Object(avahiName, g).Call(avahiEntryGroup+".AddAddress", 0, iface, proto, flags, name, addr).Err
}

func (d *dbusAvahi) Commit(g dbus.ObjectPath) error {
	return d.conn.Object(avahiName, g).Call(avahiEntryGroup+".Commit", 0).Err
}

func (d *dbusAvahi) Free(g dbus.ObjectPath) error {
	d.mu.Lock()
	delete(d.groups, g)
	d.mu.Unlock()
	return d.conn.Object(avahiName, g).Call(avahiEntryGroup+".Free", 0).Err
}

func (d *dbusAvahi) Lost() <-chan struct{} { return d.lost }

func (d *dbusAvahi) Close() error { return d.conn.Close() }
