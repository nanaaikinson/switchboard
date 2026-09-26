package linux

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/smallstep/truststore"

	"github.com/nanaaikinson/switchboard/internal/dns"
	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

// System paths. Home-relative paths are joined to Options.Home.
const (
	helperBinPath  = "/usr/local/libexec/switchboard/sb-helper"
	helperUnitName = "switchboard-helper.service"
	helperUnitPath = "/etc/systemd/system/" + helperUnitName
	daemonUnitName = "switchboard.service"
	daemonUnitRel  = ".config/systemd/user/" + daemonUnitName
	resolvedDir    = "/etc/systemd/resolved.conf.d"
	dnsmasqDir     = "/etc/NetworkManager/dnsmasq.d"
	hostsPath      = "/etc/hosts"
)

// DefaultHelperSocket is where the helper serves the listener protocol. Its
// directory is the unit's RuntimeDirectory.
const DefaultHelperSocket = "/run/switchboard/helper.sock"

// marker starts every file Switchboard writes, so uninstall only removes
// its own files.
const marker = "# Managed by Switchboard; removed by 'sb uninstall'\n"

// Options configures a Platform. UID, Home and SbPath describe the user who
// ran `sb setup`; the rest exist for tests.
type Options struct {
	UID    int
	User   string // login name; "" looks it up from UID
	Home   string
	SbPath string
	// Version is this sb's version: the helper reports it, and diagnostics
	// compare the helper's with it. "" skips the comparison.
	Version string

	Root          string                                            // prefix for every system path; "" is /
	Run           func(name string, args ...string) ([]byte, error) // nil runs the command
	Chown         func(f *os.File, uid, gid int) error              // nil is (*os.File).Chown
	HelperSocket  string                                            // "" is DefaultHelperSocket
	HelperAddrs   []string                                          // "" is ports 80 and 443 on 127.0.0.1 and [::1]
	HelperDNSAddr string                                            // "" is dns.ResolverAddr
	Anchors       string                                            // system CA anchor files, a %s pattern; "" is truststore.SystemTrustFilename
	TrustCommand  []string                                          // regenerates the system CA bundle; nil is truststore.SystemTrustCommand
	NSS           func() (posix.NSSStore, error)                    // nil is truststore.NewNSSTrust
}

// Platform implements platform.Platform for Linux with systemd.
type Platform struct {
	o     Options
	files posix.Files
}

// New returns a Platform for o.
func New(o Options) *Platform {
	if o.Root == "" {
		o.Root = "/"
	}
	if o.Run == nil {
		o.Run = func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).CombinedOutput() //nolint:gosec // G204: only called with fixed tools (systemctl, getent, ss, NetworkManager) and validated args
		}
	}
	if o.HelperSocket == "" {
		o.HelperSocket = DefaultHelperSocket
	}
	if o.HelperDNSAddr == "" {
		o.HelperDNSAddr = dns.ResolverAddr
	}
	if len(o.HelperAddrs) == 0 {
		o.HelperAddrs = []string{"127.0.0.1:80", "[::1]:80", "127.0.0.1:443", "[::1]:443"}
	}
	if o.Anchors == "" {
		o.Anchors = truststore.SystemTrustFilename
	}
	if o.TrustCommand == nil {
		o.TrustCommand = truststore.SystemTrustCommand
	}
	if o.NSS == nil {
		o.NSS = func() (posix.NSSStore, error) { return truststore.NewNSSTrust() }
	}
	return &Platform{o: o, files: posix.Files{Root: o.Root, UID: o.UID, Chown: o.Chown}}
}

func (p *Platform) fs(path string) string { return p.files.Path(path) }

func (p *Platform) daemonUnit() string { return filepath.Join(p.o.Home, daemonUnitRel) }

// dnsMode is how .<tld> names reach the daemon's DNS server.
type dnsMode int

const (
	modeResolved dnsMode = iota // systemd-resolved drop-in with a routing domain
	modeDnsmasq                 // NetworkManager's dnsmasq plugin drop-in
	modeHosts                   // /etc/hosts entries for exact names only
)

// detectDNS picks the split-DNS mechanism: systemd-resolved if it is active,
// else NetworkManager if it runs dnsmasq, else the hosts file.
func (p *Platform) detectDNS() dnsMode {
	if out, err := p.o.Run("systemctl", "is-active", "systemd-resolved"); err == nil && strings.TrimSpace(string(out)) == "active" {
		return modeResolved
	}
	if out, err := p.o.Run("NetworkManager", "--print-config"); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			if strings.ReplaceAll(strings.TrimSpace(line), " ", "") == "dns=dnsmasq" {
				return modeDnsmasq
			}
		}
	}
	return modeHosts
}

// Validate checks the options before any privileged change.
func (p *Platform) Validate() error {
	if p.o.UID <= 0 {
		return fmt.Errorf("uid %d: install for a regular user, not root", p.o.UID)
	}
	if !filepath.IsAbs(p.o.Home) || !filepath.IsAbs(p.o.SbPath) {
		return errors.New("home and sb path must be absolute")
	}
	// The path goes into a systemd ExecStart= line.
	if strings.ContainsAny(p.o.SbPath, " \t\n\"'\\%;$") {
		return fmt.Errorf("sb binary path %q has spaces or special characters; move sb to a plain path such as ~/.local/bin/sb", p.o.SbPath)
	}
	if err := p.files.CheckUserDir(p.o.Home); err != nil {
		return err
	}
	if fi, err := os.Stat(p.fs(p.o.SbPath)); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("sb binary %s is not a regular file", p.o.SbPath)
	}
	if _, err := p.o.Run("systemctl", "--version"); err != nil {
		return errors.New("systemctl not found; Switchboard needs systemd on Linux")
	}
	return nil
}

// InstallPlan lists, in order, what Install makes the system do.
func (p *Platform) InstallPlan(tld string, dnsPort int) []string {
	var dns string
	switch p.detectDNS() {
	case modeResolved:
		dns = fmt.Sprintf("Write %s so systemd-resolved sends .%s lookups to 127.0.0.1 port %d, then restart systemd-resolved", resolvedDropin(tld), tld, dnsPort)
	case modeDnsmasq:
		dns = fmt.Sprintf("Write %s so NetworkManager's dnsmasq sends .%s lookups to 127.0.0.1 port %d, then reload NetworkManager", dnsmasqDropin(tld), tld, dnsPort)
	default:
		dns = fmt.Sprintf("No systemd-resolved or NetworkManager dnsmasq found: add a Switchboard block to %s that lists each exact .%s route name. Wildcard routes (*.name.%s) will NOT resolve", hostsPath, tld, tld)
	}
	return []string{
		dns,
		fmt.Sprintf("Copy %s to %s (owned by root, mode 0755)", p.o.SbPath, helperBinPath),
		fmt.Sprintf("Write %s and start it: runs the helper as root at boot; it only binds ports 80 and 443 on 127.0.0.1 and [::1], and DNS on %s, and passes them to your daemon, and keeps the %s block up to date if there is one", helperUnitPath, p.o.HelperDNSAddr, hostsPath),
		fmt.Sprintf("Write %s and start it in your systemd user session: runs 'sb daemon' as you", p.daemonUnit()),
	}
}

// UninstallPlan lists what Uninstall removes. Missing items are skipped.
func (p *Platform) UninstallPlan(tld string) []string {
	return []string{
		fmt.Sprintf("Stop, disable and remove %s", p.daemonUnit()),
		fmt.Sprintf("Remove %s or %s (only if Switchboard wrote them) and the Switchboard block in %s", resolvedDropin(tld), dnsmasqDropin(tld), hostsPath),
		fmt.Sprintf("Stop, disable and remove %s and %s", helperUnitPath, helperBinPath),
	}
}

// TrustPlan lists what TrustCA and TrustNSS change.
func (p *Platform) TrustPlan(certPath string) []string {
	return []string{
		fmt.Sprintf("Add %s to the system CA bundle (%s), replacing any older Switchboard CA of yours, and regenerate it", certPath, p.systemBundle()),
		"As you, not root: add it to Chrome's and Firefox's certificate databases (~/.pki/nssdb, ~/.mozilla/firefox), if certutil (libnss3-tools or nss-tools) is installed",
	}
}

// UntrustPlan lists what UntrustCA and UntrustNSS change.
func (p *Platform) UntrustPlan(certPath string) []string {
	what := "every Switchboard CA of yours"
	if certPath != "" {
		what = certPath + " and any older Switchboard CA of yours"
	}
	return []string{
		fmt.Sprintf("Remove %s from the system CA bundle (%s) and regenerate it", what, p.systemBundle()),
		"As you, not root: remove it from Chrome's and Firefox's certificate databases",
	}
}

func (p *Platform) systemBundle() string {
	if p.o.Anchors == "" {
		return "none found"
	}
	return filepath.Dir(p.o.Anchors)
}
