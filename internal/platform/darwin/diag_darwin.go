package darwin

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
	"strconv"
	"strings"
	"time"

	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

var withFix = posix.WithFix

// HelperRunning reports whether the privileged helper accepts connections on
// its socket. It connects and hangs up without sending a request.
func (p *Platform) HelperRunning(ctx context.Context) error {
	d := net.Dialer{Timeout: 2 * time.Second}
	c, err := d.DialContext(ctx, "unix", p.o.HelperSocket)
	switch {
	case err == nil:
		return c.Close()
	case errors.Is(err, fs.ErrNotExist):
		if _, serr := os.Stat(p.fs(helperPlistPath)); serr != nil {
			return withFix("Run 'sb setup'.", "not installed (no %s)", helperPlistPath)
		}
		return withFix("Re-run 'sb setup' to reload it; see "+helperLogPath+" if it keeps failing.",
			"installed but not running (no socket at %s)", p.o.HelperSocket)
	default:
		return withFix("Re-run 'sb setup' to restart it; see "+helperLogPath+" if it keeps failing.",
			"not accepting connections on %s: %w", p.o.HelperSocket, err)
	}
}

// CheckResolver reports whether /etc/resolver/<tld> is Switchboard's and points
// at 127.0.0.1:port.
func (p *Platform) CheckResolver(tld string, port int) error {
	if !posix.ValidTLD(tld) {
		return fmt.Errorf("invalid tld %q", tld)
	}
	path := resolverPath(tld)
	got, err := os.ReadFile(p.fs(path))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return withFix("Run 'sb setup'.", "%s is missing", path)
	case err != nil:
		return fmt.Errorf("read %s: %w", path, err)
	case bytes.Equal(got, resolverContent(port)):
		return nil
	case isOurResolver(got):
		return withFix("Run 'sb uninstall', then 'sb setup'.",
			"%s points at the wrong port (%s), want %d", path, resolverField(got, "port"), port)
	default:
		return withFix(fmt.Sprintf("Another local DNS tool owns .%s; remove %s or that tool (e.g. 'valet uninstall'), then run 'sb setup'.", tld, path),
			"%s was written by another tool (nameserver %s port %s)", path, resolverField(got, "nameserver"), resolverField(got, "port"))
	}
}

// resolverField returns the value of the first "key value" line in a resolver
// file, or "?".
func resolverField(b []byte, key string) string {
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		if f := strings.Fields(sc.Text()); len(f) >= 2 && f[0] == key {
			return f[1]
		}
	}
	return "?"
}

// LookupHost resolves host the way apps do, through the system resolver
// (dscacheutil), and returns its IPv4 and IPv6 addresses.
func (p *Platform) LookupHost(_ context.Context, host string) ([]string, error) {
	out, err := p.o.Run("dscacheutil", "-q", "host", "-a", "name", host)
	if err != nil {
		return nil, fmt.Errorf("dscacheutil: %w: %s", err, bytes.TrimSpace(out))
	}
	var addrs []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if k = strings.TrimSpace(k); ok && (k == "ip_address" || k == "ipv6_address") {
			addrs = append(addrs, strings.TrimSpace(v))
		}
	}
	return addrs, nil
}

// PortOwner names the process listening on TCP port, as "name (pid N)", using
// lsof. It returns "" if lsof shows no listener: either the port is free or the
// holder belongs to another user (such as root) and is hidden without sudo.
func (p *Platform) PortOwner(_ context.Context, port int) (string, error) {
	out, err := p.o.Run("lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fpc")
	var exit *exec.ExitError
	if errors.As(err, &exit) && len(bytes.TrimSpace(out)) == 0 {
		return "", nil // lsof exits 1 when nothing matches
	}
	if err != nil {
		return "", fmt.Errorf("lsof: %w", err)
	}
	return parseLsof(out), nil
}

// parseLsof reads lsof -Fpc output ("p<pid>" then "c<command>" per process)
// and names the first process.
func parseLsof(out []byte) string {
	var pid, cmd string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() && cmd == "" {
		if v, ok := strings.CutPrefix(sc.Text(), "p"); ok && pid == "" {
			pid = v
		} else if v, ok := strings.CutPrefix(sc.Text(), "c"); ok && pid != "" {
			cmd = v
		}
	}
	if pid == "" {
		return ""
	}
	return fmt.Sprintf("%s (pid %s)", cmd, pid)
}
