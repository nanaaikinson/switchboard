package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/api/client"
	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/dns"
	"github.com/nanaaikinson/switchboard/internal/pki"
	"github.com/nanaaikinson/switchboard/internal/platform"
)

// doctorPorts are the web ports Switchboard serves. Swapped in tests.
var doctorPorts = []int{80, 443}

// doctorRoots verifies the HTTPS probe; nil means the system trust store,
// which is the point of the check. Swapped in tests.
var doctorRoots *x509.CertPool

// caMinLife is how long the CA must stay valid before doctor warns.
const caMinLife = 90 * 24 * time.Hour

const doctorDialTimeout = time.Second

type checkStatus string

const (
	checkPass checkStatus = "PASS"
	checkFail checkStatus = "FAIL"
	checkSkip checkStatus = "SKIP"
)

type checkResult struct {
	status       checkStatus
	name, detail string
	fix          string // set on failures
}

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose setup, DNS, ports and upstream apps",
		Long: `Check that every part of Switchboard works, and print one line per check
with a one-line fix for each failure:

  - the daemon is running
  - the privileged helper is running
  - the resolver file for each TLD exists and points at the daemon's DNS server
  - probe.<tld> resolves to 127.0.0.1 through the system resolver
  - the local CA exists, covers every TLD and is not about to expire
  - https://probe.<tld> presents a certificate the system trusts
  - ports 80 and 443 are served by Switchboard or free, else who holds them
  - each route's upstream port accepts connections

Checks the current OS does not support yet are reported as SKIP.
Exits with status 1 if any check fails.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p, _ := currentPlatform()
			results := runDoctor(cmd.Context(), p)
			out := cmd.OutOrStdout()
			failed := 0
			for _, r := range results {
				fmt.Fprintf(out, "[%s] %s: %s\n", r.status, r.name, r.detail)
				if r.status == checkFail {
					failed++
					fmt.Fprintf(out, "       fix: %s\n", r.fix)
				}
			}
			if failed > 0 {
				return fmt.Errorf("%d of %d checks failed", failed, len(results))
			}
			fmt.Fprintf(out, "All %d checks passed.\n", len(results))
			return nil
		},
	}
}

func runDoctor(ctx context.Context, p platform.Platform) []checkResult {
	var rs []checkResult

	st, stErr := daemonStatus(ctx)
	up := stErr == nil
	if up {
		rs = append(rs, pass("daemon", fmt.Sprintf("running %s, up %s", st.Version, time.Duration(st.UptimeSeconds)*time.Second)))
	} else {
		detail := stErr.Error()
		if errors.Is(stErr, client.ErrDaemonNotRunning) {
			detail = "not running"
		}
		rs = append(rs, checkResult{checkFail, "daemon", detail,
			"Re-run 'sb setup' to start it as a login service, or run 'sb daemon' in a terminal to see why it stops."})
	}

	rs = append(rs, fromErr("helper", "running", p.HelperRunning(ctx), "Run 'sb setup'."))

	tlds, dnsPort := dns.DefaultTLDs(), defaultDNSPort()
	if up {
		tlds = st.TLDs
		if len(st.DNS.Addrs) > 0 {
			if port, ok := portOf(st.DNS.Addrs[0]); ok {
				dnsPort = port
			}
		}
	}
	for _, tld := range tlds {
		resolver := fromErr("resolver ."+tld, fmt.Sprintf("split DNS for .%s is in place", tld),
			p.CheckResolver(tld, dnsPort), "Run 'sb setup'.")
		rs = append(rs, resolver)

		var fix string
		switch {
		case !up:
			fix = "Start the daemon first (see above)."
		case !st.DNS.Listening:
			fix = fmt.Sprintf("The daemon's DNS server is not listening (%s); free port %d, then re-run 'sb setup'.", st.DNS.Error, dnsPort)
		case resolver.status == checkFail:
			fix = "Fix the resolver file first (see above)."
		default:
			fix = "Flush the DNS cache (macOS: sudo dscacheutil -flushcache; sudo killall -HUP mDNSResponder; Linux: resolvectl flush-caches), then re-run 'sb doctor'."
		}
		rs = append(rs, checkLookup(ctx, p, "probe."+tld, fix))
	}

	rs = append(rs, checkCA(tlds))
	for _, tld := range tlds {
		rs = append(rs, checkHTTPS(ctx, st, up, "probe."+tld))
	}

	for _, port := range doctorPorts {
		rs = append(rs, checkPort(ctx, p, port, st, up))
	}

	return append(rs, checkRoutes(ctx, p, st, up)...)
}

// daemonStatus asks the daemon on the control socket for its status.
func daemonStatus(ctx context.Context) (api.Status, error) {
	c, err := newClient()
	if err != nil {
		return api.Status{}, err
	}
	return c.Status(ctx)
}

func pass(name, detail string) checkResult {
	return checkResult{status: checkPass, name: name, detail: detail}
}

// fromErr turns a platform diagnostic into a result. The error's own Fix()
// wins over defaultFix; unsupported checks are skipped.
func fromErr(name, okDetail string, err error, defaultFix string) checkResult {
	switch {
	case err == nil:
		return pass(name, okDetail)
	case errors.Is(err, errors.ErrUnsupported):
		return checkResult{status: checkSkip, name: name, detail: err.Error()}
	}
	fix := defaultFix
	var f interface{ Fix() string }
	if errors.As(err, &f) {
		fix = f.Fix()
	}
	return checkResult{checkFail, name, err.Error(), fix}
}

func checkLookup(ctx context.Context, p platform.Platform, host, fix string) checkResult {
	addrs, err := p.LookupHost(ctx, host)
	if err == nil && !slices.Contains(addrs, "127.0.0.1") {
		got := "nothing"
		if len(addrs) > 0 {
			got = strings.Join(addrs, ", ")
		}
		err = fmt.Errorf("resolves to %s, want 127.0.0.1", got)
	}
	return fromErr(host, "resolves to 127.0.0.1", err, fix)
}

// checkCA checks the local CA's files, name constraints and expiry.
func checkCA(tlds []string) checkResult {
	const name = "local CA"
	dir, err := pki.DefaultDir()
	var ca *pki.CA
	if err == nil {
		ca, err = pki.Load(dir)
	}
	rotate := "Run 'sb untrust', move " + filepath.Join(dir, "ca") + " aside, then run 'sb trust' to make a new CA."
	switch {
	case errors.Is(err, pki.ErrNoCA):
		return checkResult{checkFail, name, "not created yet", "Run 'sb trust' (or 'sb setup')."}
	case err != nil:
		return checkResult{checkFail, name, err.Error(), rotate}
	}
	for _, tld := range tlds {
		if !ca.Permits("x." + tld) {
			return checkResult{checkFail, name, fmt.Sprintf("cannot sign .%s names (limited to %s)", tld, strings.Join(ca.Cert.PermittedDNSDomains, ", ")), rotate}
		}
	}
	expires := ca.Cert.NotAfter.Format(time.DateOnly)
	if time.Until(ca.Cert.NotAfter) < caMinLife {
		return checkResult{checkFail, name, "expires on " + expires, rotate}
	}
	return pass(name, "valid until "+expires+", limited to ."+strings.Join(ca.Cert.PermittedDNSDomains, ", ."))
}

// checkHTTPS does a TLS handshake with the HTTPS proxy for host, verified
// against the system trust store, as a browser would.
func checkHTTPS(ctx context.Context, st api.Status, up bool, host string) checkResult {
	name := "https " + host
	switch {
	case !up:
		return checkResult{status: checkSkip, name: name, detail: "daemon not running"}
	case !st.HTTPS.Listening || len(st.HTTPS.Addrs) == 0:
		return checkResult{checkFail, name, "HTTPS proxy not listening: " + st.HTTPS.Error,
			"Re-run 'sb setup' so the helper binds port 443 and the daemon restarts."}
	}
	d := tls.Dialer{
		NetDialer: &net.Dialer{Timeout: doctorDialTimeout},
		Config:    &tls.Config{ServerName: host, RootCAs: doctorRoots, MinVersion: tls.VersionTLS12},
	}
	conn, err := d.DialContext(ctx, "tcp", st.HTTPS.Addrs[0])
	var verr *tls.CertificateVerificationError
	switch {
	case errors.As(err, &verr):
		return checkResult{checkFail, name, "certificate not trusted: " + verr.Err.Error(),
			"Run 'sb trust'. If it is already trusted, run 'sb untrust', then 'sb trust'."}
	case err != nil:
		return checkResult{checkFail, name, "TLS handshake failed: " + err.Error(),
			"Re-run 'sb setup' to restart the daemon, then run 'sb doctor' again."}
	}
	leaf := conn.(*tls.Conn).ConnectionState().PeerCertificates[0]
	_ = conn.Close()
	return pass(name, "trusted certificate, expires "+leaf.NotAfter.Format(time.DateOnly))
}

// checkPort passes if the HTTP or HTTPS proxy serves port, or if port is free
// and neither uses it. Otherwise it names the process holding the port. With
// the daemon down only a named holder fails, since the root helper may still
// hold the ports and is not visible without sudo.
func checkPort(ctx context.Context, p platform.Platform, port int, st api.Status, up bool) checkResult {
	name := "port " + strconv.Itoa(port)
	var ours []string
	proxy := st.Proxy
	for _, l := range []api.Listener{st.Proxy, st.HTTPS} {
		for _, a := range l.Addrs {
			if n, ok := portOf(a); ok && n == port {
				ours = append(ours, a)
				proxy = l
			}
		}
	}
	if proxy.Listening && len(ours) > 0 {
		return pass(name, "served by Switchboard on "+strings.Join(ours, ", "))
	}
	owner, _ := p.PortOwner(ctx, port) // best effort; "" if hidden or unsupported
	switch {
	case owner != "":
		return checkResult{checkFail, name, "held by " + owner,
			fmt.Sprintf("Stop %s or move it off port %d, then re-run 'sb setup' to restart Switchboard.", owner, port)}
	case !up:
		return checkResult{status: checkSkip, name: name, detail: "daemon not running"}
	case portBusy(ctx, port):
		return checkResult{checkFail, name, "held by a process only visible with sudo",
			fmt.Sprintf("Find it with 'sudo lsof -nP -iTCP:%d -sTCP:LISTEN', stop it, then re-run 'sb setup'.", port)}
	case len(ours) > 0:
		return checkResult{checkFail, name, "not served: " + proxy.Error,
			"Re-run 'sb setup' so the helper can bind it for the daemon."}
	default:
		return pass(name, "free (not used by Switchboard)")
	}
}

// portBusy reports whether anything accepts TCP connections on loopback port.
func portBusy(ctx context.Context, port int) bool {
	d := net.Dialer{Timeout: doctorDialTimeout}
	for _, host := range []string{"127.0.0.1", "::1"} {
		if c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port))); err == nil {
			_ = c.Close()
			return true
		}
	}
	return false
}

// checkRoutes dials every route's upstream port, and checks that names under
// wildcard routes resolve (they don't with an /etc/hosts fallback). Routes
// come from the daemon, or from the config file when the daemon is down.
func checkRoutes(ctx context.Context, p platform.Platform, st api.Status, up bool) []checkResult {
	var routes []config.Route
	if up {
		for _, r := range st.Routes {
			routes = append(routes, r.Route)
		}
	} else {
		path, err := config.DefaultPath()
		var cfg *config.Config
		if err == nil {
			cfg, err = config.Load(path)
		}
		if err != nil {
			return []checkResult{{checkFail, "routes", err.Error(), "Fix or move the config file, then re-run 'sb doctor'."}}
		}
		routes = cfg.Routes
	}
	if len(routes) == 0 {
		return []checkResult{{status: checkSkip, name: "routes", detail: "none; add one with: sb add myapp 3000"}}
	}

	rs := make([]checkResult, len(routes))
	var wg sync.WaitGroup
	for i, r := range routes {
		wg.Go(func() {
			name, upstream := displayName(r), net.JoinHostPort("127.0.0.1", strconv.Itoa(r.Port))
			d := net.Dialer{Timeout: doctorDialTimeout}
			c, err := d.DialContext(ctx, "tcp", upstream)
			if err != nil {
				rs[i] = checkResult{checkFail, name, "nothing listening on " + upstream,
					fmt.Sprintf("Start the app on port %d, or point the route at another port: sb add %s <port>", r.Port, r.Name)}
				return
			}
			_ = c.Close()
			if base, star := strings.CutPrefix(r.Name, "*."); star || r.Wildcard {
				probe := "sb-probe." + base
				addrs, err := p.LookupHost(ctx, probe)
				if !errors.Is(err, errors.ErrUnsupported) && !slices.Contains(addrs, "127.0.0.1") {
					rs[i] = checkResult{checkFail, name, fmt.Sprintf("names under it don't resolve (%s: %v %v)", probe, addrs, err),
						"Your split DNS only covers exact names (the /etc/hosts fallback). Enable systemd-resolved, then run 'sb uninstall' and 'sb setup'."}
					return
				}
			}
			rs[i] = pass(name, "upstream "+upstream+" is up")
		})
	}
	wg.Wait()
	return rs
}

// portOf returns the port of a host:port address.
func portOf(addr string) (int, bool) {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0, false
	}
	n, err := strconv.Atoi(p)
	return n, err == nil
}
