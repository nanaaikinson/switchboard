package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	dnswire "github.com/miekg/dns"
	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/api/client"
	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/dns"
	"github.com/nanaaikinson/switchboard/internal/pki"
	"github.com/nanaaikinson/switchboard/internal/platform"
)

// doctorPorts are the ports Switchboard serves: the web ports, and on
// Windows DNS too, which must be 53 there. Swapped in tests.
var doctorPorts = func() []int {
	if runtime.GOOS == "windows" {
		return []int{53, 80, 443}
	}
	return []int{80, 443}
}()

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
  - with the experimental .local mode on: routes under .local are announced
    over mDNS and resolve, and .local lookups don't leak to unicast DNS
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
		fix := "Re-run 'sb setup' to start it as a login service, or run 'sb daemon' in a terminal to see why it stops."
		switch {
		case errors.Is(stErr, client.ErrDaemonNotRunning):
			detail = "not running"
		case client.IsUntrusted(stErr):
			// Another account holds the socket or pipe: restarting won't help.
			fix = "Do what the message says; until then sb won't talk to the daemon, since whoever answers may not be yours."
		}
		rs = append(rs, checkResult{checkFail, "daemon", detail, fix})
	}

	rs = append(rs, fromErr("helper", "running", p.HelperRunning(ctx), "Run 'sb setup'."))

	tlds, mdnsTLDs, dnsPort := dns.DefaultTLDs(), configMDNSTLDs(), defaultDNSPort()
	if up {
		tlds, mdnsTLDs = st.TLDs, st.MDNS.TLDs
		if len(st.DNS.Addrs) > 0 {
			if port, ok := portOf(st.DNS.Addrs[0]); ok {
				dnsPort = port
			}
		}
	} else {
		tlds = append(tlds, mdnsTLDs...)
	}
	var unicast []string // resolved through split DNS
	for _, t := range tlds {
		if !slices.Contains(mdnsTLDs, t) {
			unicast = append(unicast, t)
		}
	}
	for _, tld := range unicast {
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
	for _, tld := range unicast {
		rs = append(rs, checkHTTPS(ctx, st, up, "probe."+tld))
	}
	if len(mdnsTLDs) > 0 {
		rs = append(rs, checkMDNS(ctx, p, st, up)...)
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
	rotate := rotateCAHint()
	switch {
	case errors.Is(err, pki.ErrNoCA):
		return checkResult{checkFail, name, "not created yet", "Run 'sb trust' (or 'sb setup')."}
	case err != nil:
		return checkResult{checkFail, name, err.Error(), rotate}
	case ca.Legacy:
		return checkResult{checkFail, name, "made by an earlier version and not limited to TLS server certificates, so its key could sign code or mail", "Run 'sb trust' to replace it."}
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

// configMDNSTLDs lists the mDNS TLDs in routes.toml, for when the daemon is
// down.
func configMDNSTLDs() []string {
	var out []string
	if path, err := config.DefaultPath(); err == nil {
		if cfg, err := config.Load(path); err == nil {
			for _, t := range cfg.TLDs {
				if t.MDNS {
					out = append(out, t.Name)
				}
			}
		}
	}
	return out
}

// checkMDNS checks the experimental .local mode: that the daemon announces
// names, that an announced name resolves, and that .local lookups stay on
// mDNS.
func checkMDNS(ctx context.Context, p platform.Platform, st api.Status, up bool) []checkResult {
	const name = "mdns (experimental)"
	var rs []checkResult
	switch {
	case !up:
		rs = append(rs, checkResult{status: checkSkip, name: name, detail: "daemon not running"})
	case st.MDNS.Error != "":
		rs = append(rs, checkResult{checkFail, name, ".local names are not announced: " + st.MDNS.Error,
			"Start the system's mDNS responder (macOS: mDNSResponder; Linux: 'sudo systemctl enable --now avahi-daemon'); the daemon retries every 10 seconds."})
	case st.MDNS.Backend == "":
		rs = append(rs, checkResult{status: checkSkip, name: name, detail: "starting; re-run 'sb doctor' in a moment"})
	default:
		rs = append(rs, pass(name, fmt.Sprintf("announcing %d .local names via %s on %s", st.MDNS.Announced, st.MDNS.Backend, st.MDNS.Interface)))
		for _, r := range st.Routes {
			if r.MDNS == api.MDNSAnnounced {
				rs = append(rs, checkLookup(ctx, p, r.Name,
					"The name is announced but the system resolver doesn't find it over mDNS; restart the mDNS responder, then re-run 'sb doctor'."))
				break
			}
		}
	}
	return append(rs, checkLocalLeak(ctx, p))
}

// unicastServers lists the unicast DNS servers (host:port) that programs
// bypassing the system resolver use; swapped in tests.
var unicastServers = func() ([]string, error) {
	cfg, err := dnswire.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil {
		return nil, err
	}
	out := make([]string, len(cfg.Servers))
	for i, s := range cfg.Servers {
		out[i] = net.JoinHostPort(s, cfg.Port)
	}
	return out, nil
}

// checkLocalLeak reports whether .local lookups can reach unicast DNS: from
// the system resolver's configuration, and by asking the unicast servers
// directly for a random .local name, as Go or musl programs and dig do.
func checkLocalLeak(ctx context.Context, p platform.Platform) checkResult {
	const name = ".local lookups"
	err := p.CheckLocalDNS(ctx)
	if err != nil && !errors.Is(err, errors.ErrUnsupported) {
		return fromErr(name, "", err, "Keep .local lookups on mDNS, or use .test names instead.")
	}
	server, probe, addrs := unicastAnswersLocal(ctx)
	if server != "" {
		return checkResult{checkFail, name,
			fmt.Sprintf("DNS server %s answers .local names (%s -> %s): programs that skip mDNS get another host's address, and the server sees the .local names you look up", server, probe, strings.Join(addrs, ", ")),
			"Use .test names for anything that must not leave this machine, or ask your network admin to stop answering .local (it is reserved for mDNS, RFC 6762)."}
	}
	if err != nil {
		return fromErr(name, "", err, "")
	}
	return pass(name, "stay on mDNS; unicast DNS doesn't answer them")
}

// unicastAnswersLocal asks each unicast DNS server for a random .local
// name. It returns the first server that answers, the name and the answer.
func unicastAnswersLocal(ctx context.Context) (server, probe string, addrs []string) {
	servers, err := unicastServers()
	if err != nil {
		return "", "", nil // no resolv.conf (Windows): nothing to ask
	}
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	probe = "sb-leak-" + hex.EncodeToString(b) + ".local"
	m := new(dnswire.Msg)
	m.SetQuestion(probe+".", dnswire.TypeA)
	c := dnswire.Client{Timeout: doctorDialTimeout}
	for _, s := range servers {
		in, _, err := c.ExchangeContext(ctx, m, s)
		if err != nil || in.Rcode != dnswire.RcodeSuccess {
			continue
		}
		for _, rr := range in.Answer {
			switch a := rr.(type) {
			case *dnswire.A:
				addrs = append(addrs, a.A.String())
			case *dnswire.CNAME:
				addrs = append(addrs, a.Target)
			}
		}
		if len(addrs) > 0 {
			return s, probe, addrs
		}
	}
	return "", "", nil
}

// rotateCAHint says how to replace the local CA, e.g. to cover a new TLD.
func rotateCAHint() string {
	dir, err := pki.DefaultDir()
	if err != nil {
		dir = "<config dir>/pki"
	}
	return "Run 'sb untrust', move " + filepath.Join(dir, "ca") + " aside, run 'sb trust' to make a new CA, then re-run 'sb setup' to restart the daemon with it."
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
	for _, l := range []api.Listener{st.Proxy, st.HTTPS, st.DNS} {
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
	case strings.HasPrefix(owner, "http.sys"):
		return checkResult{checkFail, name, "held by " + owner,
			fmt.Sprintf("Something registered port %d with Windows' http.sys (see 'netsh http show servicestate'). Stop it (IIS: 'iisreset /stop' or remove the site's binding; other apps: their settings), then run 'sb setup' again.", port)}
	case strings.Contains(owner, "run by another account"):
		return checkResult{checkFail, name, "held by " + owner,
			fmt.Sprintf("Another account's process holds port %d, so it could see or answer the traffic meant for Switchboard. Have an administrator stop it, then re-run 'sb setup'.", port)}
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
	mdnsTLDs := st.MDNS.TLDs
	if !up {
		mdnsTLDs = configMDNSTLDs()
	}

	rs := make([]checkResult, len(routes))
	var wg sync.WaitGroup
	for i, r := range routes {
		wg.Go(func() {
			name, upstream := displayName(r), net.JoinHostPort("127.0.0.1", strconv.Itoa(r.Port))
			d := net.Dialer{Timeout: doctorDialTimeout}
			c, err := d.DialContext(ctx, "tcp", upstream)
			if err != nil {
				fix := fmt.Sprintf("Start the app on port %d, or point the route at another port: sb add %s <port>", r.Port, r.Name)
				if r.File != "" {
					fix = fmt.Sprintf("Start the app on port %d, or change the port in %s and run 'sb apply'", r.Port, shortPath(r.File))
				}
				rs[i] = checkResult{checkFail, name, "nothing listening on " + upstream, fix}
				return
			}
			_ = c.Close()
			base, star := strings.CutPrefix(r.Name, "*.")
			overMDNS := slices.ContainsFunc(mdnsTLDs, func(t string) bool { return strings.HasSuffix(base, "."+t) })
			if (star || r.Wildcard) && overMDNS {
				rs[i] = pass(name, "upstream "+upstream+" is up; names under it can't be announced over mDNS")
				return
			}
			if star || r.Wildcard {
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
