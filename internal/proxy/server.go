package proxy

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// DefaultAddrs are the HTTP listen addresses: IPv4 and IPv6 loopback on port 80.
func DefaultAddrs() []string { return []string{"127.0.0.1:80", "[::1]:80"} }

// DefaultTLSAddrs are the HTTPS listen addresses: loopback on port 443.
func DefaultTLSAddrs() []string { return []string{"127.0.0.1:443", "[::1]:443"} }

// shutdownTimeout bounds how long Serve waits for in-flight requests.
const shutdownTimeout = 5 * time.Second

// Listen binds TCP on each loopback addr. Empty addrs use DefaultAddrs.
// Ports below 1024 need the privileged helper, which passes listeners to Serve.
func Listen(addrs []string) ([]net.Listener, error) {
	if len(addrs) == 0 {
		addrs = DefaultAddrs()
	}
	lns := make([]net.Listener, 0, len(addrs))
	for _, addr := range addrs {
		if err := checkLoopback(addr); err != nil {
			closeAll(lns)
			return nil, err
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			closeAll(lns)
			return nil, fmt.Errorf("proxy: listen %s: %w; is another web server using this port? Run 'sb doctor'", addr, err)
		}
		lns = append(lns, ln)
	}
	return lns, nil
}

// Serve serves h on every listener until ctx is done or one fails, then shuts
// down gracefully. Serve takes ownership of the listeners.
func Serve(ctx context.Context, h http.Handler, lns []net.Listener) error {
	return serve(ctx, h, lns, nil)
}

// ServeTLS is Serve over TLS with conf, which must provide certificates.
// HTTP/2 is offered through ALPN alongside HTTP/1.1.
func ServeTLS(ctx context.Context, h http.Handler, lns []net.Listener, conf *tls.Config) error {
	if conf == nil {
		return errors.New("proxy: ServeTLS needs a TLS config")
	}
	return serve(ctx, h, lns, conf.Clone())
}

func serve(ctx context.Context, h http.Handler, lns []net.Listener, conf *tls.Config) error {
	if len(lns) == 0 {
		return errors.New("proxy: Serve needs at least one listener")
	}
	srv := &http.Server{
		Handler:           h,
		TLSConfig:         conf,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No read/write timeouts: SSE and WebSockets are long-lived.
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelDebug),
	}

	errc := make(chan error, len(lns))
	var wg sync.WaitGroup
	for _, ln := range lns {
		slog.Info("proxy listening", "addr", ln.Addr().String(), "tls", conf != nil)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if conf != nil {
				errc <- srv.ServeTLS(ln, "", "") // certificates come from conf
			} else {
				errc <- srv.Serve(ln)
			}
		}()
	}

	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	if serr := srv.Shutdown(sctx); serr != nil && err == nil {
		err = serr
	}
	wg.Wait()
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("proxy: serve: %w", err)
	}
	return nil
}

func closeAll(lns []net.Listener) {
	for _, ln := range lns {
		_ = ln.Close()
	}
}

func checkLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("proxy: invalid address %q: %w; use host:port like 127.0.0.1:80", addr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("proxy: address %q is not a loopback IP; use 127.0.0.1 or ::1", addr)
	}
	return nil
}
