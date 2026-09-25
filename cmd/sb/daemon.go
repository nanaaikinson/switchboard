package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/dns"
	"github.com/nanaaikinson/switchboard/internal/proxy"
)

type daemonOptions struct {
	dnsAddr        string
	httpAddrs      []string
	healthInterval time.Duration
	ready          func() // called once the control socket is accepting; for tests
}

func newDaemonCmd() *cobra.Command {
	var opts daemonOptions
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run the Switchboard daemon (DNS, proxy, control API)",
		Long: `Run the background daemon. It loads routes from the config file, answers
DNS for Switchboard TLDs, proxies HTTP by Host header, health-checks every
upstream port and serves the control API on a Unix socket in the config dir.

Normally started by the OS service manager, not by hand. If the DNS server or
proxy cannot bind (for example, port 80 without the helper), the daemon keeps
running and reports the error in 'sb ls' and GET /v1/status.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return runDaemon(ctx, opts)
		},
	}
	f := cmd.Flags()
	f.StringVar(&opts.dnsAddr, "dns-addr", dns.DefaultAddr, "DNS listen address (loopback only)")
	f.StringSliceVar(&opts.httpAddrs, "http-addr", proxy.DefaultAddrs(), "HTTP proxy listen addresses (loopback only)")
	f.DurationVar(&opts.healthInterval, "health-interval", api.DefaultHealthInterval, "how often to check upstream ports")
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
	svc, err := api.NewService(api.Options{
		ConfigPath: cfgPath, Config: cfg, Proxy: px, TLDs: tlds,
		Version: version, HealthInterval: opts.healthInterval,
	})
	if err != nil {
		return err
	}

	// Claim the socket first so a second daemon exits before touching ports.
	sock, err := api.DefaultSocketPath()
	if err != nil {
		return err
	}
	ctl, err := api.ListenUnix(sock)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Go(func() { svc.Run(ctx) })

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

	if lns, err := proxy.Listen(opts.httpAddrs); err != nil {
		slog.Warn("proxy not listening", "err", err)
		svc.SetProxy(api.Listener{Addrs: opts.httpAddrs, Error: err.Error()})
	} else {
		addrs := listenerAddrs(lns)
		svc.SetProxy(api.Listener{Addrs: addrs, Listening: true})
		wg.Go(func() {
			if err := proxy.Serve(ctx, px, lns); err != nil {
				slog.Error("proxy stopped", "err", err)
				svc.SetProxy(api.Listener{Addrs: addrs, Error: err.Error()})
			}
		})
	}

	slog.Info("daemon started", "version", version, "routes", len(cfg.Routes), "socket", sock)
	if opts.ready != nil {
		opts.ready()
	}
	err = api.Serve(ctx, svc, ctl)
	cancel()
	wg.Wait()
	slog.Info("daemon stopped")
	return err
}

func listenerAddrs(lns []net.Listener) []string {
	out := make([]string, len(lns))
	for i, ln := range lns {
		out[i] = ln.Addr().String()
	}
	return out
}
