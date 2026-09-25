package proxy

import (
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
)

var pages = template.Must(template.New("page").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} · Switchboard</title>
<style>
:root{color-scheme:light dark;--fg:#1d1d1f;--muted:#6e6e73;--bg:#fbfbfd;--line:#d2d2d7}
@media (prefers-color-scheme:dark){:root{--fg:#f5f5f7;--muted:#a1a1a6;--bg:#1d1d1f;--line:#424245}}
body{margin:0;padding:48px 16px;font:16px/1.5 system-ui,sans-serif;color:var(--fg);background:var(--bg)}
main{max-width:640px;margin:0 auto}h1{font-size:24px;margin:0 0 8px}p{color:var(--muted)}
code{font:14px ui-monospace,monospace}table{border-collapse:collapse;width:100%;margin-top:24px}
td,th{text-align:left;padding:8px 0;border-bottom:1px solid var(--line)}a{color:inherit}
</style></head><body><main>
<h1>{{.Title}}</h1>
{{range .Lines}}<p>{{.}}</p>{{end}}
{{with .Routes}}<table><tr><th>Name</th><th>Port</th></tr>
{{range .}}<tr><td><a href="//{{.Link}}/">{{.Name}}</a></td><td><code>{{.Port}}</code></td></tr>{{end}}
</table>{{end}}
</main></body></html>`))

type pageRoute struct {
	Name, Link string
	Port       int
}

type page struct {
	Title  string
	Lines  []string
	Routes []pageRoute
}

// notFound lists configured routes, but only for hosts under a routed TLD so
// a DNS-rebinding page (evil.com -> 127.0.0.1) cannot read the route table.
func notFound(w http.ResponseWriter, host string, t *table) {
	pg := page{Title: "No route for " + host}
	if host == "" {
		pg.Title = "No route"
	}
	if !t.suffixes[host[strings.LastIndexByte(host, '.')+1:]] {
		pg.Lines = []string{"Switchboard does not serve this host."}
		render(w, http.StatusNotFound, pg)
		return
	}
	pg.Lines = []string{"Add one with: sb add " + host + " <port>"}
	for _, r := range t.routes {
		name := strings.ToLower(r.Name)
		link := strings.TrimPrefix(name, "*.")
		if r.Wildcard && !strings.HasPrefix(name, "*.") {
			name = name + " (+ *." + name + ")"
		}
		pg.Routes = append(pg.Routes, pageRoute{Name: name, Link: link, Port: r.Port})
	}
	if len(pg.Routes) == 0 {
		pg.Lines = append(pg.Lines, "No routes are configured yet.")
	}
	render(w, http.StatusNotFound, pg)
}

// badGateway explains that the upstream port could not be reached.
func badGateway(w http.ResponseWriter, port int, notListening bool) {
	pg := page{Title: "Nothing is listening on port " + strconv.Itoa(port)}
	if notListening {
		pg.Lines = []string{
			"Switchboard found this route, but no app accepted the connection on 127.0.0.1:" + strconv.Itoa(port) + ".",
			"Start your app on port " + strconv.Itoa(port) + ", or point the route at the right port with sb add.",
		}
	} else {
		pg.Title = "Upstream on port " + strconv.Itoa(port) + " failed"
		pg.Lines = []string{"The app on 127.0.0.1:" + strconv.Itoa(port) + " closed the connection or sent an invalid response. Check its logs."}
	}
	render(w, http.StatusBadGateway, pg)
}

func render(w http.ResponseWriter, status int, pg page) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := pages.Execute(w, pg); err != nil {
		slog.Debug("proxy render page", "err", err)
	}
}

// pausedPage answers every request while Switchboard is paused.
func pausedPage(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Retry-After", "30")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><meta name="color-scheme" content="light dark"><title>Switchboard is paused</title>` +
		`<body style="font:16px system-ui;max-width:32rem;margin:15vh auto;padding:0 1rem"><h1 style="font-size:1.25rem">Switchboard is paused</h1>` +
		`<p>Every route is off for now. Resume from the tray menu, or run <code>sb resume</code>.</p></body>`))
}
