package linux

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

// ErrForeignFile means a file Switchboard would manage exists with content it
// did not write, so it is left alone.
var ErrForeignFile = errors.New("not written by Switchboard")

// maxHostsNames bounds the hosts block the helper will write.
const maxHostsNames = 2000

func resolvedDropin(tld string) string { return resolvedDir + "/switchboard-" + tld + ".conf" }
func dnsmasqDropin(tld string) string  { return dnsmasqDir + "/switchboard-" + tld + ".conf" }

func resolvedContent(tld string, port int) []byte {
	return []byte(marker + "[Resolve]\nDNS=127.0.0.1:" + strconv.Itoa(port) + "\nDomains=~" + tld + "\n")
}

func dnsmasqContent(tld string, port int) []byte {
	return []byte(marker + "server=/" + tld + "/127.0.0.1#" + strconv.Itoa(port) + "\n")
}

// InstallResolver sends .<tld> lookups to 127.0.0.1:port with the mechanism
// detectDNS picks. It fails if another tool owns the file it would write.
func (p *Platform) InstallResolver(tld string, port int) error {
	if !posix.ValidTLD(tld) || port < 1 || port > 65535 {
		return fmt.Errorf("install resolver: invalid tld %q or port %d", tld, port)
	}
	switch p.detectDNS() {
	case modeResolved:
		if err := p.writeDropin(resolvedDir, resolvedDropin(tld), resolvedContent(tld, port), tld); err != nil {
			return err
		}
		return p.run("restart systemd-resolved", "systemctl", "restart", "systemd-resolved")
	case modeDnsmasq:
		if err := p.writeDropin(dnsmasqDir, dnsmasqDropin(tld), dnsmasqContent(tld, port), tld); err != nil {
			return err
		}
		return p.reloadNetworkManager()
	default:
		return p.addHostsBlock(tld)
	}
}

func (p *Platform) writeDropin(dir, path string, want []byte, tld string) error {
	switch got, err := os.ReadFile(p.fs(path)); {
	case err == nil && bytes.Equal(got, want):
		return nil
	case err == nil && !bytes.HasPrefix(got, []byte(marker)):
		return fmt.Errorf("install resolver: %s exists: %w; another tool may own .%s. Remove it or use another TLD", path, ErrForeignFile, tld)
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("install resolver: %w", err)
	}
	if err := os.MkdirAll(p.fs(dir), 0o755); err != nil { //nolint:gosec // G301: system convention for config drop-in dirs
		return fmt.Errorf("install resolver: %w", err)
	}
	return p.files.WriteFile(path, want, 0o644, 0, 0)
}

func (p *Platform) reloadNetworkManager() error {
	if _, err := p.o.Run("nmcli", "general", "reload", "dns-full"); err == nil {
		return nil
	}
	return p.run("reload NetworkManager", "systemctl", "reload", "NetworkManager")
}

// RemoveResolver removes whatever InstallResolver wrote for tld, in any mode,
// and only files Switchboard wrote. Missing files are not an error.
func (p *Platform) RemoveResolver(tld string) error {
	if !posix.ValidTLD(tld) {
		return fmt.Errorf("remove resolver: invalid tld %q", tld)
	}
	var errs []error
	removed, err := p.removeDropin(resolvedDropin(tld))
	errs = append(errs, err)
	if removed {
		errs = append(errs, p.run("restart systemd-resolved", "systemctl", "restart", "systemd-resolved"))
	}
	removed, err = p.removeDropin(dnsmasqDropin(tld))
	errs = append(errs, err)
	if removed {
		errs = append(errs, p.reloadNetworkManager())
	}
	errs = append(errs, p.removeHostsBlock())
	return errors.Join(errs...)
}

func (p *Platform) removeDropin(path string) (bool, error) {
	got, err := os.ReadFile(p.fs(path))
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("remove resolver: %w", err)
	case !bytes.HasPrefix(got, []byte(marker)):
		return false, fmt.Errorf("left %s in place: %w", path, ErrForeignFile)
	}
	return true, p.files.Remove(path)
}

func (p *Platform) run(what, name string, args ...string) error {
	if out, err := p.o.Run(name, args...); err != nil {
		return fmt.Errorf("%s: %w: %s", what, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// The hosts block. Its header records the TLDs it may hold, so the helper
// never writes names outside them:
//
//	# BEGIN Switchboard tlds=test; removed by 'sb uninstall'
//	127.0.0.1 myapp.test
//	::1 myapp.test
//	# END Switchboard
const (
	hostsBegin = "# BEGIN Switchboard tlds="
	hostsEnd   = "# END Switchboard"
)

type hostsFile struct {
	before, after []byte
	tlds          []string
	found         bool
}

func parseHosts(data []byte) hostsFile {
	var h hostsFile
	lines := bytes.SplitAfter(data, []byte("\n"))
	for i, line := range lines {
		rest, ok := strings.CutPrefix(strings.TrimSpace(string(line)), hostsBegin)
		if !ok {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.TrimSpace(string(lines[j])) == hostsEnd {
				h.found = true
				h.before = bytes.Join(lines[:i], nil)
				h.after = bytes.Join(lines[j+1:], nil)
				list, _, _ := strings.Cut(rest, ";")
				for _, t := range strings.Split(list, ",") {
					if posix.ValidTLD(t) {
						h.tlds = append(h.tlds, t)
					}
				}
				return h
			}
		}
	}
	h.before = data
	return h
}

func renderHostsBlock(tlds, names []string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s%s; removed by 'sb uninstall'\n", hostsBegin, strings.Join(tlds, ","))
	for _, n := range names {
		fmt.Fprintf(&b, "127.0.0.1 %s\n::1 %s\n", n, n)
	}
	b.WriteString(hostsEnd + "\n")
	return b.Bytes()
}

func probeNames(tlds []string) []string {
	out := make([]string, len(tlds))
	for i, t := range tlds {
		out[i] = "probe." + t
	}
	return out
}

func (p *Platform) readHosts() ([]byte, error) {
	data, err := os.ReadFile(p.fs(hostsPath))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", hostsPath, err)
	}
	return data, nil
}

// writeHosts rewrites /etc/hosts in place. It is not renamed over, because
// containers often bind-mount it.
func (p *Platform) writeHosts(data []byte) error {
	if err := os.WriteFile(p.fs(hostsPath), data, 0o644); err != nil { //nolint:gosec // G306: /etc/hosts is world-readable
		return fmt.Errorf("write %s: %w", hostsPath, err)
	}
	return nil
}

func (p *Platform) addHostsBlock(tld string) error {
	data, err := p.readHosts()
	if err != nil {
		return err
	}
	h := parseHosts(data)
	if h.found && slices.Contains(h.tlds, tld) {
		return nil
	}
	tlds := append(h.tlds, tld)
	before := h.before
	if len(before) > 0 && !bytes.HasSuffix(before, []byte("\n")) {
		before = append(before, '\n')
	}
	return p.writeHosts(slices.Concat(before, renderHostsBlock(tlds, probeNames(tlds)), h.after))
}

func (p *Platform) removeHostsBlock() error {
	data, err := p.readHosts()
	if err != nil {
		return err
	}
	h := parseHosts(data)
	if !h.found {
		return nil
	}
	return p.writeHosts(slices.Concat(h.before, h.after))
}

// syncHosts is the helper's "hosts" op: it replaces the names in the hosts
// block, if there is one. Every name must be a plain hostname under one of
// the block's TLDs, so the user's daemon cannot make root map other domains.
func (p *Platform) syncHosts(names []string) error {
	data, err := p.readHosts()
	if err != nil {
		return err
	}
	h := parseHosts(data)
	if !h.found {
		return nil // split DNS is handled elsewhere; nothing to do
	}
	if len(names) > maxHostsNames {
		return fmt.Errorf("too many names (%d, max %d)", len(names), maxHostsNames)
	}
	all := probeNames(h.tlds)
	for _, n := range names {
		if !config.ValidHostname(n) || strings.HasPrefix(n, "*.") || !underTLD(n, h.tlds) {
			return fmt.Errorf("refusing hosts entry %q: not a hostname under .%s", n, strings.Join(h.tlds, ", ."))
		}
		all = append(all, n)
	}
	slices.Sort(all)
	all = slices.Compact(all)
	return p.writeHosts(slices.Concat(h.before, renderHostsBlock(h.tlds, all), h.after))
}

func underTLD(name string, tlds []string) bool {
	for _, t := range tlds {
		if strings.HasSuffix(name, "."+t) {
			return true
		}
	}
	return false
}
