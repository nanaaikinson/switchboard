package linux

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

// HelperRunning reports whether the privileged helper accepts connections on
// its socket. It connects and hangs up without sending a request.
func (p *Platform) HelperRunning(ctx context.Context) error {
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "unix", p.o.HelperSocket)
	logs := "see 'journalctl -u " + helperUnitName + "' if it keeps failing"
	switch {
	case err == nil:
		return c.Close()
	case errors.Is(err, fs.ErrNotExist):
		if _, serr := os.Stat(p.fs(helperUnitPath)); serr != nil {
			return posix.WithFix("Run 'sb setup'.", "not installed (no %s)", helperUnitPath)
		}
		return posix.WithFix("Re-run 'sb setup' to restart it; "+logs+".", "installed but not running (no socket at %s)", p.o.HelperSocket)
	default:
		return posix.WithFix("Re-run 'sb setup' to restart it; "+logs+".", "not accepting connections on %s: %w", p.o.HelperSocket, err)
	}
}

// CheckResolver reports whether split DNS for tld is in place and points at
// 127.0.0.1:port, in whichever mode setup chose.
func (p *Platform) CheckResolver(tld string, port int) error {
	if !posix.ValidTLD(tld) {
		return fmt.Errorf("invalid tld %q", tld)
	}
	reinstall := "Run 'sb uninstall', then 'sb setup'."
	for _, f := range []struct {
		path string
		want []byte
		svc  string
	}{
		{resolvedDropin(tld), resolvedContent(tld, port), "systemd-resolved"},
		{dnsmasqDropin(tld), dnsmasqContent(tld, port), "NetworkManager"},
	} {
		got, err := os.ReadFile(p.fs(f.path))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return fmt.Errorf("read %s: %w", f.path, err)
		case !bytes.HasPrefix(got, []byte(marker)):
			return posix.WithFix(fmt.Sprintf("Another tool owns .%s; remove %s or that tool, then run 'sb setup'.", tld, f.path),
				"%s was written by another tool", f.path)
		case !bytes.Equal(got, f.want):
			return posix.WithFix(reinstall, "%s does not point at 127.0.0.1:%d", f.path, port)
		}
		if out, err := p.o.Run("systemctl", "is-active", f.svc); err != nil || strings.TrimSpace(string(out)) != "active" {
			return posix.WithFix("Start it with 'sudo systemctl start "+f.svc+"', or run 'sb uninstall' then 'sb setup' to pick another mode.",
				"%s is in place but %s is not running", f.path, f.svc)
		}
		return nil
	}
	data, err := p.readHosts()
	if err != nil {
		return err
	}
	if h := parseHosts(data); h.found && slices.Contains(h.tlds, tld) {
		return nil // hosts mode: exact names only
	}
	return posix.WithFix("Run 'sb setup'.", "no split DNS for .%s (no %s, %s or %s block)", tld, resolvedDropin(tld), dnsmasqDropin(tld), hostsPath)
}

// CheckLocalDNS reads the hosts line of /etc/nsswitch.conf. .local lookups
// stay on mDNS when an mdns module that ends the lookup ([NOTFOUND=return])
// or systemd-resolved (which never sends .local to unicast DNS) comes before
// dns; otherwise names Avahi doesn't know go on to unicast DNS.
func (p *Platform) CheckLocalDNS(context.Context) error {
	const path = "/etc/nsswitch.conf"
	b, err := os.ReadFile(p.fs(path))
	if errors.Is(err, fs.ErrNotExist) {
		return posix.WithFix("Install libnss-mdns (nss-mdns) or enable systemd-resolved, so .local names are looked up over mDNS only.",
			"%s is missing, so .local lookups go to unicast DNS", path)
	}
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	line := hostsLine(b)
	if localLeaks(line) {
		return posix.WithFix(`Put "mdns4_minimal [NOTFOUND=return]" (from libnss-mdns) or "resolve [!UNAVAIL=return]" before "dns" on the hosts line of `+path+`; until then prefer .test names.`,
			"%s (hosts: %s) passes .local names mDNS can't answer to unicast DNS, which sees them and can answer with another host's address", path, line)
	}
	return nil
}

// hostsLine returns the sources of the "hosts:" line of nsswitch.conf.
func hostsLine(b []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "hosts" {
			return strings.Join(strings.Fields(v), " ")
		}
	}
	return ""
}

// localLeaks reports whether a hosts line can send .local names to dns.
func localLeaks(hosts string) bool {
	fields := strings.Fields(hosts)
	for i, f := range fields {
		action := ""
		if i+1 < len(fields) && strings.HasPrefix(fields[i+1], "[") {
			action = strings.ToUpper(fields[i+1])
		}
		switch {
		case f == "dns":
			return true
		case strings.HasPrefix(f, "mdns") && strings.Contains(action, "NOTFOUND=RETURN"):
			return false
		case f == "resolve":
			return false // systemd-resolved keeps .local on mDNS
		}
	}
	return false
}

// LookupHost resolves host through NSS (getent), as apps do, and returns its
// addresses.
func (p *Platform) LookupHost(_ context.Context, host string) ([]string, error) {
	out, err := p.o.Run("getent", "ahosts", host)
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		return nil, nil // not found
	}
	if err != nil {
		return nil, fmt.Errorf("getent: %w: %s", err, bytes.TrimSpace(out))
	}
	var addrs []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) > 0 && !slices.Contains(addrs, f[0]) {
			addrs = append(addrs, f[0])
		}
	}
	return addrs, nil
}

var ssUser = regexp.MustCompile(`users:\(\("([^"]+)",pid=(\d+)`)

// PortOwner names the process listening on TCP port, as "name (pid N)",
// using ss. It returns "" if ss shows no listener, or the listener belongs to
// another user (such as root), whose process ss hides without sudo.
func (p *Platform) PortOwner(_ context.Context, port int) (string, error) {
	out, err := p.o.Run("ss", "-H", "-ltnp", "sport = :"+strconv.Itoa(port))
	if err != nil {
		return "", fmt.Errorf("ss: %w", err)
	}
	if m := ssUser.FindSubmatch(out); m != nil {
		return fmt.Sprintf("%s (pid %s)", m[1], m[2]), nil
	}
	return "", nil
}
