package proxy

import (
	"net"
	"net/http"
	"strconv"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// RedirectHTTPS wraps p for the plain-HTTP listeners: requests for a route
// with RedirectHTTPS set get a 307 to the same URL over HTTPS on httpsPort;
// everything else is served by p. 307 keeps the method and body, and unlike
// 301/308 browsers don't cache it, so turning a route's redirect off takes
// effect at once.
func RedirectHTTPS(p Proxy, httpsPort int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS == nil {
			// Wildcard routes match any first label, so only echo real
			// hostnames into Location. (Go already rejects "/" and "@".)
			host := normalizeHost(r.Host)
			if route, _, ok := p.Lookup(host); ok && route.RedirectHTTPS && config.ValidHostname(host) {
				if httpsPort != 443 {
					host = net.JoinHostPort(host, strconv.Itoa(httpsPort))
				}
				http.Redirect(w, r, "https://"+host+r.URL.RequestURI(), http.StatusTemporaryRedirect) //nolint:gosec // G710: host is a validated hostname under a routed name
				return
			}
		}
		p.ServeHTTP(w, r)
	})
}
