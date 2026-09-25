package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/dashboard"
	"github.com/nanaaikinson/switchboard/internal/dns"
	"github.com/nanaaikinson/switchboard/internal/docker"
	"github.com/nanaaikinson/switchboard/internal/pki"
	"github.com/nanaaikinson/switchboard/internal/platform"
	"github.com/nanaaikinson/switchboard/internal/proxy"
	webui "github.com/nanaaikinson/switchboard/ui/dashboard"
)

type daemonOptions struct {
	dnsAddr        string
	httpAddrs      []string
	httpsAddrs     []string
	healthInterval time.Duration
	useHelper      bool // ask the privileged helper for the HTTP listeners first
	// dockerConnect discovers containers; nil disables Docker routes.
	dockerConnect func(context.Context) (docker.Client, string, error)
	ready         func() // called once the control socket is accepting; for tests
}

func newDaemonCmd() *cobra.Command {
	var opts daemonOptions
	var useDocker bool
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the Switchboard daemon (DNS, proxy, control API)",
		Long: `Run the background daemon. It loads routes from the config file, answers
DNS for Switchboard TLDs, proxies HTTP by Host header, health-checks every
upstream port and serves the control API on a Unix socket in the config dir.

Normally started by the OS service manager, not by hand. If the DNS server or
proxy cannot bind (for example, port 80 without the helper), the daemon keeps
running and reports the error in 'sb ls' and GET /v1/status.

HTTPS is served with certificates issued on demand by the local CA in
<config dir>/pki, which is created on first run. Plain HTTP requests for routes
with redirect_https are redirected to HTTPS while HTTPS is up.

Unless --http-addr or --https-addr is given, the daemon first asks the
privileged helper installed by 'sb setup' for the port 80 and 443 listeners,
and binds any it did not get itself.

With --docker (the default), running containers that publish a TCP port get
routes too: <container>.<tld>, or <service>.<project>.<tld> for Compose. They
are never saved to routes.toml. The daemon finds Docker through DOCKER_HOST,
else the Docker, OrbStack, Colima, Rancher Desktop and Podman default sockets,
and retries quietly every 10 seconds while none is running. See 'sb ls'.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if useDocker {
				opts.dockerConnect = docker.Connect
			}
			opts.useHelper = !cmd.Flags().Changed("http-addr") && !cmd.Flags().Changed("https-addr")
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runDaemon(ctx, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.dnsAddr, "dns-addr", dns.DefaultAddr, "DNS listen address (loopback only)")
	f.StringSliceVar(&opts.httpAddrs, "http-addr", proxy.DefaultAddrs(), "HTTP proxy listen addresses (loopback only)")
	f.StringSliceVar(&opts.httpsAddrs, "https-addr", proxy.DefaultTLSAddrs(), "HTTPS proxy listen addresses (loopback only)")
	f.DurationVar(&opts.healthInterval, "health-interval", api.DefaultHealthInterval, "how often to check upstream ports")
	f.BoolVar(&useDocker, "docker", true, "route running Docker containers that publish a TCP port")
	return cmd
}

func runDaemon(ctx context.Context, opts daemonOptions) error {
	cfgPath, err := config.DefaultPath()
	if err != nil {
		return err
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return err
	}
	tlds := dns.DefaultTLDs()

	px, err := proxy.New(cfg.Routes)
	if err != nil {
		return fmt.Errorf("load routes from %s: %w", cfgPath, err)
	}
	dashHosts := make([]string, len(tlds))
	for i, t := range tlds {
		dashHosts[i] = dashboard.Host(t)
	}
	// The CA comes first: the API reports on it. Without one, HTTPS stays off.
	issuer, ca, caErr := newIssuer(tlds, px)
	hosts := &hostsSyncer{Proxy: px, next: make(chan []config.Route, 1), extra: dashHosts}
	svc, err := api.NewService(api.Options{
		ConfigPath: cfgPath, Config: cfg, Proxy: hosts, TLDs: tlds,
		Version: version, HealthInterval: opts.healthInterval,
		Reserved: dashHosts,
		Logs:     px.Logs,
		CA:       func() api.CAInfo { return caInfo(ca, issuer, caErr, dashHosts[0]) },
		Pause:    px.SetPaused,
	})
	if err != nil {
		return err
	}
	dash := dashboard.New(api.Handler(svc), webui.Assets())
	// The control socket also mints dashboard logins; the dashboard itself can't.
	control := http.NewServeMux()
	control.Handle("/", api.Handler(svc))
	control.Handle("POST /v1/dashboard/login", dash.LoginHandler())

	// Claim the socket first so a second daemon exits before touching ports.
	sock, err := api.DefaultSocketPath()
	if err != nil {
		return err
	}
	ctl, err := api.ListenControl(sock)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() { svc.Run(ctx) })
	wg.Go(func() { hosts.run(ctx, platform.Current()) })
	if opts.dockerConnect != nil {
		svc.SetDocker(api.DockerStatus{Enabled: true}, nil)
		w := &docker.Watcher{Connect: opts.dockerConnect, TLDs: tlds, OnChange: func(st docker.State) {
			svc.SetDocker(dockerStatus(st))
		}}
		wg.Go(func() { w.Run(ctx) })
	}

	d, err := dns.New(opts.dnsAddr, tlds)
	if err != nil {
		_ = ctl.Close()
		return err
	}
	if err := d.Listen(); err != nil {
		slog.Warn("dns server not listening", "err", err)
		svc.SetDNS(api.Listener{Addrs: []string{opts.dnsAddr}, Error: err.Error()})
	} else {
		svc.SetDNS(api.Listener{Addrs: []string{d.Addr()}, Listening: true})
		wg.Go(func() {
			if err := d.Serve(ctx); err != nil {
				slog.Error("dns server stopped", "err", err)
				svc.SetDNS(api.Listener{Addrs: []string{d.Addr()}, Error: err.Error()})
			}
		})
	}

	plain, secure := proxyListeners(ctx, opts)
	// HTTPS first, so plain HTTP knows whether and where to redirect.
	httpsPort := 0
	if secure.err == nil {
		if caErr != nil {
			closeListeners(secure.lns)
			secure.err = caErr
		} else {
			addrs := listenerAddrs(secure.lns)
			httpsPort = secure.lns[0].Addr().(*net.TCPAddr).Port
			svc.SetHTTPS(api.Listener{Addrs: addrs, Listening: true})
			h := dash.Wrap(px, dashHosts, httpsPort)
			wg.Go(func() {
				if err := proxy.ServeTLS(ctx, h, secure.lns, issuer.TLSConfig()); err != nil {
					slog.Error("https proxy stopped", "err", err)
					svc.SetHTTPS(api.Listener{Addrs: addrs, Error: err.Error()})
				}
			})
		}
	}
	if secure.err != nil {
		slog.Warn("https proxy not listening", "err", secure.err)
		svc.SetHTTPS(api.Listener{Addrs: opts.httpsAddrs, Error: secure.err.Error()})
	}

	if plain.err != nil {
		slog.Warn("proxy not listening", "err", plain.err)
		svc.SetProxy(api.Listener{Addrs: opts.httpAddrs, Error: plain.err.Error()})
	} else {
		var h http.Handler = px
		if httpsPort != 0 {
			h = proxy.RedirectHTTPS(px, httpsPort)
		}
		h = dash.Wrap(h, dashHosts, httpsPort) // plain HTTP to the dashboard goes to HTTPS
		addrs := listenerAddrs(plain.lns)
		svc.SetProxy(api.Listener{Addrs: addrs, Listening: true})
		wg.Go(func() {
			if err := proxy.Serve(ctx, h, plain.lns); err != nil {
				slog.Error("proxy stopped", "err", err)
				svc.SetProxy(api.Listener{Addrs: addrs, Error: err.Error()})
			}
		})
	}

	slog.Info("daemon started", "version", version, "routes", len(cfg.Routes), "socket", sock)
	if opts.ready != nil {
		opts.ready()
	}
	err = api.ServeHandler(ctx, control, ctl)
	cancel()
	wg.Wait()
	slog.Info("daemon stopped")
	return err
}

type listenerSet struct {
	lns []net.Listener
	err error
}

// proxyListeners returns the plain HTTP and the HTTPS listeners. When allowed,
// it asks the helper first and treats its port 443 sockets as HTTPS; any set
// the helper did not provide is bound directly from opts.
func proxyListeners(ctx context.Context, opts daemonOptions) (plain, secure listenerSet) {
	if opts.useHelper {
		lns, err := platform.Current().HelperListeners(ctx)
		if err == nil {
			for _, ln := range lns {
				if ln.Addr().(*net.TCPAddr).Port == 443 {
					secure.lns = append(secure.lns, ln)
				} else {
					plain.lns = append(plain.lns, ln)
				}
			}
			slog.Info("proxy listeners received from helper", "http", len(plain.lns), "https", len(secure.lns))
		} else {
			slog.Info("helper unavailable; binding proxy ports directly", "err", err)
		}
	}
	if len(plain.lns) == 0 {
		plain.lns, plain.err = proxy.Listen(opts.httpAddrs)
	}
	if len(secure.lns) == 0 {
		secure.lns, secure.err = proxy.Listen(opts.httpsAddrs)
		if secure.err != nil && opts.useHelper {
			secure.err = fmt.Errorf("%w; re-run 'sb setup' so the helper binds port 443", secure.err)
		}
	}
	return plain, secure
}

// newIssuer loads or creates the local CA and serves names that only match
// through a wildcard route with a wildcard certificate for their parent, so
// one certificate covers all siblings.
func newIssuer(tlds []string, px proxy.Proxy) (*pki.Issuer, *pki.CA, error) {
	dir, err := pki.DefaultDir()
	if err != nil {
		return nil, nil, err
	}
	ca, err := pki.LoadOrCreate(dir, tlds)
	if err != nil {
		return nil, nil, fmt.Errorf("local CA: %w", err)
	}
	for _, tld := range tlds {
		if !ca.Permits("x." + tld) {
			slog.Warn("local CA does not cover a TLD; run 'sb doctor'", "tld", tld)
		}
	}
	return pki.NewIssuer(ca, pki.IssuerOptions{NameFor: func(host string) string {
		if _, wildcard, ok := px.Lookup(host); ok && wildcard {
			if _, parent, _ := strings.Cut(host, "."); strings.Contains(parent, ".") {
				return "*." + parent
			}
		}
		return host
	}}), ca, nil
}

// caInfo reports the CA for GET /v1/ca. Trust is checked by verifying a
// certificate for host against the system trust store, as a browser would.
func caInfo(ca *pki.CA, issuer *pki.Issuer, caErr error, host string) api.CAInfo {
	if caErr != nil {
		return api.CAInfo{Error: caErr.Error()}
	}
	info := api.CAInfo{
		Present: true, Fingerprint: ca.Fingerprint(), TLDs: ca.Cert.PermittedDNSDomains,
		NotAfter: ca.Cert.NotAfter.UTC().Format(time.RFC3339),
	}
	leaf, err := issuer.Certificate(host)
	if err == nil {
		_, err = leaf.Leaf.Verify(x509.VerifyOptions{DNSName: host, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}})
	}
	if err != nil {
		info.Error = err.Error()
	} else {
		info.Trusted = true
	}
	return info
}

func closeListeners(lns []net.Listener) {
	for _, ln := range lns {
		_ = ln.Close()
	}
}

func listenerAddrs(lns []net.Listener) []string {
	out := make([]string, len(lns))
	for i, ln := range lns {
		out[i] = ln.Addr().String()
	}
	return out
}

// hostsSyncer passes route changes to the proxy, then on to the helper's
// /etc/hosts block, which is the Linux fallback when there is no split DNS.
// The latest table wins, and syncing never blocks a route change.
type hostsSyncer struct {
	proxy.Proxy
	next  chan []config.Route // capacity 1
	extra []string            // names the daemon serves itself, like the dashboard
}

// SetRoutes implements api.RouteSetter. Callers are serialized by the service.
func (h *hostsSyncer) SetRoutes(routes []config.Route) error {
	if err := h.Proxy.SetRoutes(routes); err != nil {
		return err
	}
	select {
	case <-h.next: // drop a table the syncer has not picked up yet
	default:
	}
	h.next <- slices.Clone(routes)
	return nil
}

func (h *hostsSyncer) run(ctx context.Context, p platform.Platform) {
	for {
		var routes []config.Route
		select {
		case <-ctx.Done():
			return
		case routes = <-h.next:
		}
		names := slices.Clone(h.extra)
		for _, r := range routes {
			if !strings.HasPrefix(r.Name, "*.") { // wildcards cannot go in a hosts file
				names = append(names, r.Name)
			}
		}
		err := p.SyncHosts(ctx, names)
		switch {
		case errors.Is(err, errors.ErrUnsupported):
			return // this OS never needs it
		case err != nil:
			slog.Debug("hosts sync failed", "err", err)
		}
	}
}

// dockerStatus converts the watcher's state for the API.
func dockerStatus(st docker.State) (api.DockerStatus, []api.DockerRoute) {
	out := api.DockerStatus{Enabled: true, Connected: st.Connected, Endpoint: st.Endpoint, Error: st.Err}
	for _, sk := range st.Skipped {
		out.Skipped = append(out.Skipped, api.DockerSkip{Container: sk.Container, Reason: sk.Reason})
	}
	routes := make([]api.DockerRoute, len(st.Routes))
	for i, r := range st.Routes {
		routes[i] = api.DockerRoute{Route: r.Route, Container: r.Container}
	}
	return out, routes
}
