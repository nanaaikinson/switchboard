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
