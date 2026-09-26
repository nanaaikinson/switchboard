package windows

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/windows"

	"github.com/nanaaikinson/switchboard/internal/mdns"
	"github.com/nanaaikinson/switchboard/internal/pki"
)

// ErrUnsupported matches errors.ErrUnsupported: Windows doesn't need the
// feature (the helper service and its socket handoff, hosts syncing).
var ErrUnsupported error = unsupportedError{}

type unsupportedError struct{}

func (unsupportedError) Error() string        { return "not needed on Windows" }
func (unsupportedError) Is(target error) bool { return target == errors.ErrUnsupported }

// AnotherAccount marks a PortOwner held by a process of another account.
const AnotherAccount = "run by another account"

// ErrForeignRule means something Switchboard would manage exists and isn't
// Switchboard's, so it is left alone.
var ErrForeignRule = errors.New("not created by Switchboard")

// Options configures a Platform.
type Options struct {
	User   string // DOMAIN\name the daemon's logon task runs as
	SID    string // that user's SID; "" is the current user's
	SbPath string
	// PowerShell runs a script; nil runs powershell.exe. For tests.
	PowerShell func(script string) ([]byte, error)
	// Store is the LocalMachine\Root store; nil is the real one. For tests.
	Store RootStore
}

// RootStore adds, lists and removes certificates in a Windows certificate
// store.
type RootStore interface {
	Add(cert *x509.Certificate) error
	List() ([]*x509.Certificate, error)
	Remove(cert *x509.Certificate) (bool, error)
}

// Platform implements platform.Platform for Windows. There is no helper
// service: Windows has no privileged ports, so the daemon binds 53, 80 and
// 443 itself, and setup elevates once through UAC for the NRPT rule, the
// certificate and the logon task.
type Platform struct{ o Options }

// New returns a Platform for o.
func New(o Options) *Platform {
	if o.PowerShell == nil {
		o.PowerShell = runPowerShell
	}
	if o.Store == nil {
		o.Store = localMachineRoot{}
	}
	return &Platform{o: o}
}

func runPowerShell(script string) ([]byte, error) {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	cmd.Stdin = strings.NewReader(script)
	return cmd.CombinedOutput()
}

func (p *Platform) ps(what, script string) (string, error) {
	out, err := p.o.PowerShell(script)
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("%s: %w: %s", what, err, text)
	}
	return text, nil
}

func (p *Platform) sid() (string, error) {
	if p.o.SID != "" {
		return p.o.SID, nil
	}
	return CurrentSID()
}

// OpenURL opens rawURL, already validated as http(s), in the default browser.
func (p *Platform) OpenURL(rawURL string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start() //nolint:gosec // G204: fixed binary; URL validated by platform.OpenURL
}

// Validate checks the options before any privileged change.
func (p *Platform) Validate() error {
	if !filepath.IsAbs(p.o.SbPath) {
		return fmt.Errorf("sb path %q must be absolute", p.o.SbPath)
	}
	if strings.ContainsAny(p.o.SbPath, `"`) {
		return fmt.Errorf("sb path %q can't contain quotes", p.o.SbPath)
	}
	if fi, err := os.Stat(p.o.SbPath); err != nil || !fi.Mode().IsRegular() {
		return fmt.Errorf("sb binary %s is not a regular file", p.o.SbPath)
	}
	if p.o.User == "" {
		return errors.New("no user to run the daemon as")
	}
	return nil
}

// InstallPlan lists what Install makes the system do.
func (p *Platform) InstallPlan(tld string, dnsPort int) []string {
	sid, _ := p.sid()
	return []string{
		fmt.Sprintf("Add an NRPT rule (Name Resolution Policy Table) that sends .%s lookups to 127.0.0.1, where the daemon's DNS server listens on port %d", tld, dnsPort),
		fmt.Sprintf("Register the Scheduled Task %s%s, which runs '%s daemon' without a window when %s logs on, and start it now", TaskPath, TaskName(sid), p.o.SbPath, p.o.User),
	}
}

// UninstallPlan lists what Uninstall removes. Missing items are skipped.
func (p *Platform) UninstallPlan(tld string) []string {
	sid, _ := p.sid()
	return []string{
		fmt.Sprintf("Stop and remove the Scheduled Task %s%s (only if Switchboard created it)", TaskPath, TaskName(sid)),
		fmt.Sprintf("Remove Switchboard's NRPT rule for .%s (other rules are left alone)", tld),
	}
}

// TrustPlan lists what TrustCA changes.
func (p *Platform) TrustPlan(certPath string) []string {
	return []string{fmt.Sprintf("Add %s to the LocalMachine\\Root certificate store, which Edge, Chrome and Firefox trust, for TLS server certificates only, replacing any older Switchboard CA of yours", certPath)}
}

// UntrustPlan lists what UntrustCA changes.
func (p *Platform) UntrustPlan(certPath string) []string {
	what := "every Switchboard CA of yours"
	if certPath != "" {
		what = certPath + " and any older Switchboard CA of yours"
	}
	return []string{fmt.Sprintf("Remove %s from the LocalMachine\\Root certificate store", what)}
}

// InstallResolver adds the NRPT rule for tld. NRPT names DNS servers by IP
// only, so port must be 53.
func (p *Platform) InstallResolver(tld string, port int) error {
	if !validTLD(tld) {
		return fmt.Errorf("install resolver: invalid tld %q", tld)
	}
	if port != 53 {
		return fmt.Errorf("install resolver: NRPT rules can only use DNS port 53, not %d", port)
	}
	out, err := p.ps("add NRPT rule", nrptScript("add", tld))
	if strings.Contains(out, "FOREIGN") {
		return fmt.Errorf("install resolver: an NRPT rule for .%s exists: %w; another tool may own .%s. Remove it (Get-DnsClientNrptRule) or use another TLD", tld, ErrForeignRule, tld)
	}
	return err
}

// RemoveResolver removes Switchboard's NRPT rule for tld, leaving others.
func (p *Platform) RemoveResolver(tld string) error {
	if !validTLD(tld) {
		return fmt.Errorf("remove resolver: invalid tld %q", tld)
	}
	out, err := p.ps("remove NRPT rule", nrptScript("remove", tld))
	if err == nil && strings.Contains(out, "FOREIGN") {
		return fmt.Errorf("left another tool's NRPT rule for .%s in place: %w", tld, ErrForeignRule)
	}
	return err
}

// InstallService registers and starts the daemon's logon task.
func (p *Platform) InstallService() error {
	sid, err := p.sid()
	if err != nil {
		return err
	}
	_, err = p.ps("register the logon task", taskScript(p.o.User, sid, p.o.SbPath))
	return err
}

// RemoveService stops and removes the logon task if Switchboard made it.
func (p *Platform) RemoveService() error {
	sid, err := p.sid()
	if err != nil {
		return err
	}
	out, err := p.ps("remove the logon task", untaskScript(sid))
	if strings.Contains(out, "FOREIGN") {
		return fmt.Errorf("left the task %s%s in place: %w", TaskPath, TaskName(sid), ErrForeignRule)
	}
	return err
}

// RestartDaemon restarts the daemon's logon task. It reports false if there
// is no task.
func (p *Platform) RestartDaemon() (bool, error) {
	sid, err := p.sid()
	if err != nil {
		return false, err
	}
	out, err := p.ps("restart the daemon", restartScript(sid))
	if err != nil {
		return true, err
	}
	return !strings.Contains(out, "GONE"), nil
}

// TrustCA adds the CA to LocalMachine\Root, trusted for TLS server
// authentication only, after removing the user's older Switchboard CAs.
// Privileged (UAC). Only a Switchboard CA made by this user with the
// fingerprint the user confirmed is trusted.
func (p *Platform) TrustCA(certPath, fingerprint string) error {
	cert, err := readCA(certPath, pki.ValidateNow)
	if err != nil {
		return fmt.Errorf("trust CA: %w", err)
	}
	sid, err := p.sid()
	if err != nil {
		return fmt.Errorf("trust CA: %w", err)
	}
	if err := pki.CheckTrust(cert, fingerprint, sid); err != nil {
		return fmt.Errorf("trust CA: %w", err)
	}
	if err := p.o.Store.Add(cert); err != nil {
		return fmt.Errorf("trust CA in LocalMachine\\Root: %w", err)
	}
	// The user's older CAs stay trusted until this one is.
	if err := p.removeCAs(func(c *x509.Certificate) bool { return !c.Equal(cert) && p.owns(c, sid) }); err != nil {
		return fmt.Errorf("trusted the CA, but could not remove older Switchboard CAs: %w; run 'sb trust' again", err)
	}
	return nil
}

// UntrustCA removes every Switchboard CA this user made from
// LocalMachine\Root, and the CA at certPath if given. Privileged (UAC).
func (p *Platform) UntrustCA(certPath string) error {
	var given *x509.Certificate
	if certPath != "" {
		c, err := readCA(certPath, pki.ValidateRemovable) // an expired or older CA must still be removable
		if err != nil {
			return fmt.Errorf("untrust CA: %w", err)
		}
		given = c
	}
	sid, err := p.sid()
	if err != nil {
		return fmt.Errorf("untrust CA: %w", err)
	}
	if err := p.removeCAs(func(c *x509.Certificate) bool { return p.owns(c, sid) || given != nil && c.Equal(given) }); err != nil {
		return fmt.Errorf("untrust CA in LocalMachine\\Root: %w", err)
	}
	return nil
}

// owns reports whether c is a Switchboard CA made by the user with sid.
func (p *Platform) owns(c *x509.Certificate, sid string) bool {
	return pki.OwnedBy(c, sid, p.o.User)
}

// removeCAs removes the Switchboard CAs in LocalMachine\Root that match.
func (p *Platform) removeCAs(match func(*x509.Certificate) bool) error {
	certs, err := p.o.Store.List()
	if err != nil {
		return err
	}
	for _, c := range certs {
		if pki.IsSwitchboardCA(c) && match(c) {
			if _, err := p.o.Store.Remove(c); err != nil {
				return err
			}
		}
	}
	return nil
}

// TrustNSS does nothing: Firefox on Windows trusts LocalMachine\Root
// ("enterprise roots"), and Chrome uses the Windows store.
func (p *Platform) TrustNSS(string) error { return nil }

// UntrustNSS does nothing; see TrustNSS.
func (p *Platform) UntrustNSS(string) error { return nil }

// ServeHelper is unsupported: there is no helper service on Windows.
func (p *Platform) ServeHelper(context.Context) error { return ErrUnsupported }

// HelperListeners is unsupported, so the daemon binds its ports itself.
func (p *Platform) HelperListeners(context.Context) ([]net.Listener, error) {
	return nil, ErrUnsupported
}

// HelperDNS is unsupported, so the daemon binds its DNS port itself.
func (p *Platform) HelperDNS(context.Context) (net.PacketConn, net.Listener, error) {
	return nil, nil, ErrUnsupported
}

// SyncHosts is unsupported: NRPT covers every name, wildcards included.
func (p *Platform) SyncHosts(context.Context, []string) error { return ErrUnsupported }

// AdminCommand runs argv elevated: Windows shows its UAC prompt.
func (p *Platform) AdminCommand(argv []string, _ string) (*exec.Cmd, error) {
	if len(argv) == 0 {
		return nil, errors.New("admin command: empty argv")
	}
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-Command", "-")
	cmd.Stdin = strings.NewReader(adminScript(argv))
	return cmd, nil
}

// HelperRunning reports that no helper is needed.
func (p *Platform) HelperRunning(context.Context) error {
	return fmt.Errorf("Windows needs no helper: the daemon binds its ports itself (%w)", errors.ErrUnsupported) //nolint:staticcheck // ST1005: starts with a proper noun
}

// MDNS reports that Windows has no responder sb can use; the built-in one
// is used instead.
func (p *Platform) MDNS() (mdns.Backend, error) {
	return mdns.Backend{}, fmt.Errorf("a native mDNS responder is %w on Windows", errors.ErrUnsupported)
}

// CheckLocalDNS is not implemented on Windows yet.
func (p *Platform) CheckLocalDNS(context.Context) error {
	return fmt.Errorf("checking where .local lookups go is %w on Windows", errors.ErrUnsupported)
}

// CheckResolver reports whether Switchboard's NRPT rule for tld is in place.
func (p *Platform) CheckResolver(tld string, port int) error {
	if port != 53 {
		return withFix("Run the daemon with its DNS server on 127.0.0.1:53 (the default on Windows).", "the DNS server is on port %d, but NRPT rules can only use 53", port)
	}
	out, err := p.ps("read NRPT rules", nrptScript("show", tld))
	if err != nil {
		return err
	}
	rules, err := parseNRPT([]byte(out))
	if err != nil {
		return err
	}
	for _, r := range rules {
		if r.Comment != Marker {
			return withFix(fmt.Sprintf("Another tool owns .%s: remove its rule (Get-DnsClientNrptRule | Remove-DnsClientNrptRule), then run 'sb setup'.", tld),
				"an NRPT rule for .%s from another tool sends it to %s", tld, strings.Join(r.NameServers, ", "))
		}
	}
	for _, r := range rules {
		for _, ns := range r.NameServers {
			if ns == "127.0.0.1" {
				return nil
			}
		}
	}
	if len(rules) > 0 {
		return withFix("Run 'sb uninstall', then 'sb setup'.", "Switchboard's NRPT rule for .%s doesn't point at 127.0.0.1", tld)
	}
	return withFix("Run 'sb setup'.", "no NRPT rule for .%s", tld)
}

// LookupHost resolves host with the Windows resolver, which applies NRPT.
func (p *Platform) LookupHost(ctx context.Context, host string) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return nil, nil
	}
	return addrs, err
}

// PortOwner names the process listening on TCP (else UDP) port, and says so
// when it runs as another account: Windows has no privileged ports, so any
// user can bind 53, 80 or 443 before the daemon and see or answer its
// traffic. Port owned by PID 4 (System) belongs to http.sys, so it asks
// netsh which request queue has a URL registered on the port, e.g. IIS's
// DefaultAppPool.
func (p *Platform) PortOwner(_ context.Context, port int) (string, error) {
	out, err := p.ps("find the port's owner", listenerScript(port))
	if err != nil || out == "" {
		return "", err
	}
	fields := strings.SplitN(out, "\t", 3)
	pid, _ := strconv.Atoi(strings.TrimSpace(fields[0]))
	var name, account string
	if len(fields) > 1 {
		name = strings.TrimSpace(fields[1])
	}
	if len(fields) > 2 {
		account = strings.TrimSpace(fields[2])
	}
	if pid != 4 {
		if p.o.User != "" && !strings.EqualFold(account, p.o.User) {
			if account == "" {
				account = "an account whose processes you can't see"
			}
			return fmt.Sprintf("%s (pid %d, %s: %s)", name, pid, AnotherAccount, account), nil
		}
		return fmt.Sprintf("%s (pid %d)", name, pid), nil
	}
	state, err := exec.Command("netsh", "http", "show", "servicestate", "view=requestq").Output()
	if err != nil {
		return "http.sys (see 'netsh http show servicestate')", nil
	}
	return describeHTTPSys(QueuesOnPort(parseServiceState(string(state)), port), port), nil
}

// readCA reads the CA certificate and checks it with validate. It runs
// elevated on a path in the user's profile, so it opens the file itself, never
// a link or junction there, even one swapped in at the last moment.
func readCA(path string, validate func(*x509.Certificate) error) (*x509.Certificate, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("CA path %q is not absolute", path)
	}
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	f := os.NewFile(uintptr(h), path)
	defer f.Close()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return nil, err
	}
	cert, err := pki.ParseCert(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if err := validate(cert); err != nil {
		return nil, fmt.Errorf("%s is not a Switchboard CA: %w", path, err)
	}
	return cert, nil
}
