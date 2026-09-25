package darwin

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/smallstep/truststore"

	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

// ErrForeignFile means a file Switchboard would manage exists with content it
// did not write, so it is left alone.
var ErrForeignFile = errors.New("not written by Switchboard")

// System paths. Home-relative paths are joined to Options.Home.
const (
	helperBinPath   = "/Library/PrivilegedHelperTools/" + HelperLabel
	helperPlistPath = "/Library/LaunchDaemons/" + HelperLabel + ".plist"
	helperLogPath   = "/var/log/switchboard-helper.log"
	helperSockDir   = "/var/run/switchboard"
	resolverDir     = "/etc/resolver"
	agentPlistRel   = "Library/LaunchAgents/" + DaemonLabel + ".plist"
	agentLogRel     = "Library/Logs/switchboard-daemon.log"
)

// DefaultHelperSocket is where the helper serves the listener protocol.
const DefaultHelperSocket = helperSockDir + "/helper.sock"

// Options configures a Platform. UID, Home and SbPath describe the user who
// ran `sb setup`; the rest exist for tests.
type Options struct {
	UID    int
	Home   string
	SbPath string

	Root         string                                            // prefix for every system path; "" is /
	Run          func(name string, args ...string) ([]byte, error) // nil runs the command
	Chown        func(f *os.File, uid, gid int) error              // nil is (*os.File).Chown
	HelperSocket string                                            // "" is DefaultHelperSocket
	HelperAddrs  []string                                          // "" is ports 80 and 443 on 127.0.0.1 and [::1]
	Trust        func(*x509.Certificate) error                     // nil is truststore.Install (System keychain)
	NSS          func() (NSSStore, error)                          // nil is truststore.NewNSSTrust
	DNSSDSocket  string                                            // "" is mDNSResponder's socket
}

// Platform implements platform.Platform for macOS.
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
			return exec.Command(name, args...).CombinedOutput() //nolint:gosec // G204: only called with fixed tools (launchctl, security, dscacheutil, lsof) and validated args
		}
	}
	if o.Chown == nil {
		o.Chown = (*os.File).Chown
	}
	if o.HelperSocket == "" {
		o.HelperSocket = DefaultHelperSocket
	}
	if len(o.HelperAddrs) == 0 {
		o.HelperAddrs = []string{"127.0.0.1:80", "[::1]:80", "127.0.0.1:443", "[::1]:443"}
	}
	if o.Trust == nil {
		o.Trust = func(c *x509.Certificate) error { return truststore.Install(c) }
	}
	if o.NSS == nil {
		o.NSS = defaultNSS
	}
	return &Platform{o: o, files: posix.Files{Root: o.Root, UID: o.UID, Chown: o.Chown}}
}

func (p *Platform) fs(path string) string { return p.files.Path(path) }

func (p *Platform) agentPlist() string { return filepath.Join(p.o.Home, agentPlistRel) }
func (p *Platform) agentLog() string   { return filepath.Join(p.o.Home, agentLogRel) }

func resolverPath(tld string) string { return filepath.Join(resolverDir, tld) }

// Validate checks the options before any privileged change.
func (p *Platform) Validate() error {
	if p.o.UID <= 0 {
		return fmt.Errorf("uid %d: install for a regular user, not root", p.o.UID)
	}
	if !filepath.IsAbs(p.o.Home) || !filepath.IsAbs(p.o.SbPath) {
		return errors.New("home and sb path must be absolute")
	}
	if err := p.files.CheckUserDir(p.o.Home); err != nil {
		return err
	}
	if fi, err := os.Stat(p.fs(p.o.SbPath)); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("sb binary %s is not a regular file", p.o.SbPath)
	}
	return nil
}

// InstallPlan lists, in order, what Install makes the system do.
func (p *Platform) InstallPlan(tld string, dnsPort int) []string {
	return []string{
		fmt.Sprintf("Write %s so .%s names resolve via 127.0.0.1 port %d", resolverPath(tld), tld, dnsPort),
		fmt.Sprintf("Copy %s to %s (owned by root, mode 0755)", p.o.SbPath, helperBinPath),
		fmt.Sprintf("Write %s and load it: runs the helper as root at boot; it only binds ports 80 and 443 on 127.0.0.1 and [::1] and passes them to your daemon", helperPlistPath),
		fmt.Sprintf("Write %s and load it: runs 'sb daemon' as you at login", p.agentPlist()),
	}
}

// UninstallPlan lists what Uninstall removes. Missing items are skipped.
func (p *Platform) UninstallPlan(tld string) []string {
	return []string{
		fmt.Sprintf("Unload and remove %s and %s", p.agentPlist(), p.agentLog()),
		fmt.Sprintf("Remove %s (only if Switchboard wrote it), and %s if it is then empty", resolverPath(tld), resolverDir),
		fmt.Sprintf("Unload and remove %s, %s, %s and %s", helperPlistPath, helperBinPath, helperLogPath, helperSockDir),
	}
}

// InstallResolver points the OS resolver for tld at 127.0.0.1:port. It is a
// no-op if Switchboard already wrote the same file, and fails if another tool
// owns it.
func (p *Platform) InstallResolver(tld string, port int) error {
	if !posix.ValidTLD(tld) || port < 1 || port > 65535 {
		return fmt.Errorf("install resolver: invalid tld %q or port %d", tld, port)
	}
	path, want := resolverPath(tld), resolverContent(port)
	switch got, err := os.ReadFile(p.fs(path)); {
	case err == nil && bytes.Equal(got, want):
		return nil
	case err == nil:
		return fmt.Errorf("install resolver: %s exists: %w; another tool (such as Laravel Valet) may own .%s. Remove it or use another TLD",
			path, ErrForeignFile, tld)
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("install resolver: %w", err)
	}
	if err := os.MkdirAll(p.fs(resolverDir), 0o755); err != nil { //nolint:gosec // G301: system convention for /etc/resolver
		return fmt.Errorf("install resolver: %w", err)
	}
	return p.files.WriteFile(path, want, 0o644, 0, 0)
}

// RemoveResolver removes the resolver file for tld if Switchboard wrote it,
// then removes /etc/resolver if it is empty. Missing files are not an error.
func (p *Platform) RemoveResolver(tld string) error {
	if !posix.ValidTLD(tld) {
		return fmt.Errorf("remove resolver: invalid tld %q", tld)
	}
	path := resolverPath(tld)
	got, err := os.ReadFile(p.fs(path))
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("remove resolver: %w", err)
	case !isOurResolver(got):
		return fmt.Errorf("left %s in place: %w", path, ErrForeignFile)
	default:
		if err := os.Remove(p.fs(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove resolver: %w", err)
		}
	}
	_ = os.Remove(p.fs(resolverDir)) // fails harmlessly unless empty
	return nil
}

func isOurResolver(b []byte) bool {
	rest, ok := strings.CutPrefix(string(b), resolverMarker+"nameserver 127.0.0.1\nport ")
	if !ok {
		return false
	}
	port, err := strconv.Atoi(strings.TrimSuffix(rest, "\n"))
	return err == nil && bytes.Equal(b, resolverContent(port))
}

// InstallService installs and loads the root helper LaunchDaemon and the
// user's daemon LaunchAgent. Re-running replaces both.
func (p *Platform) InstallService() error {
	gid, err := posix.UserGID(p.o.UID)
	if err != nil {
		return err
	}
	if err := p.files.CopyRootOwned(p.o.SbPath, helperBinPath); err != nil {
		return err
	}
	if err := p.files.WriteFile(helperPlistPath, helperPlist(helperBinPath, p.o.UID, helperLogPath), 0o644, 0, 0); err != nil {
		return err
	}
	if err := p.reload("system", HelperLabel, helperPlistPath); err != nil {
		return err
	}

	if err := p.files.CheckUserDir(filepath.Join(p.o.Home, "Library")); err != nil {
		return err
	}
	agentDir := filepath.Dir(p.agentPlist())
	if err := p.files.MkdirOwned(agentDir, gid); err != nil {
		return err
	}
	if err := p.files.CheckUserDir(agentDir); err != nil {
		return err
	}
	if err := p.files.WriteFile(p.agentPlist(), agentPlist(p.o.SbPath, p.agentLog()), 0o644, p.o.UID, gid); err != nil {
		return err
	}
	return p.reload(p.guiDomain(), DaemonLabel, p.agentPlist())
}

// RemoveService unloads and deletes everything InstallService created. It is
// safe to run when nothing is installed.
func (p *Platform) RemoveService() error {
	var errs []error
	errs = append(errs, p.unload(p.guiDomain(), DaemonLabel))
	for _, f := range []string{p.agentPlist(), p.agentLog()} {
		errs = append(errs, p.files.RemoveUserFile(f))
	}
	errs = append(errs, p.unload("system", HelperLabel))
	for _, f := range []string{helperPlistPath, helperBinPath, helperLogPath} {
		errs = append(errs, p.files.Remove(f))
	}
	if err := os.RemoveAll(p.fs(helperSockDir)); err != nil {
		errs = append(errs, fmt.Errorf("remove %s: %w", helperSockDir, err))
	}
	return errors.Join(errs...)
}

func (p *Platform) guiDomain() string { return "gui/" + strconv.Itoa(p.o.UID) }

// reload boots out any loaded copy of the job, then bootstraps plist.
func (p *Platform) reload(domain, label, plistPath string) error {
	if err := p.unload(domain, label); err != nil {
		return err
	}
	if out, err := p.o.Run("launchctl", "bootstrap", domain, plistPath); err != nil {
		return fmt.Errorf("launchctl bootstrap %s %s: %w: %s", domain, plistPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// unload boots out domain/label. Not being loaded is success; still being
// loaded afterwards is an error.
func (p *Platform) unload(domain, label string) error {
	target := domain + "/" + label
	if _, err := p.o.Run("launchctl", "print", target); err != nil {
		return nil // not loaded
	}
	out, err := p.o.Run("launchctl", "bootout", target)
	if _, perr := p.o.Run("launchctl", "print", target); perr == nil {
		return fmt.Errorf("launchctl bootout %s: still loaded (%w: %s)", target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// RestartDaemon restarts the daemon's LaunchAgent, so it runs the sb binary
// now at its path. It reports false, and does nothing, if the daemon isn't
// installed as a service.
func (p *Platform) RestartDaemon() (bool, error) {
	target := p.guiDomain() + "/" + DaemonLabel
	if _, err := p.o.Run("launchctl", "print", target); err != nil {
		return false, nil
	}
	if out, err := p.o.Run("launchctl", "kickstart", "-k", target); err != nil {
		return true, fmt.Errorf("launchctl kickstart -k %s: %w: %s", target, err, strings.TrimSpace(string(out)))
	}
	return true, nil
}
