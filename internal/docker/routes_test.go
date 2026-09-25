package docker

import (
	"fmt"
	"strings"
	"testing"
)

var tlds = []string{"test"}

func ctr(name string, labels map[string]string, ports ...Port) Container {
	return Container{ID: "id-" + name, Names: []string{"/" + name}, Labels: labels, Ports: ports}
}

func pub(host, container int) Port {
	return Port{IP: "0.0.0.0", PrivatePort: container, PublicPort: host, Type: "tcp"}
}

func compose(project, service string, extra ...string) map[string]string {
	l := map[string]string{labelComposeProject: project, labelComposeService: service}
	for i := 0; i+1 < len(extra); i += 2 {
		l[extra[i]] = extra[i+1]
	}
	return l
}

func summary(routes []Route, skips []Skip) string {
	var parts []string
	for _, r := range routes {
		parts = append(parts, fmt.Sprintf("%s->%d(%s)", r.Name, r.Port, r.Container))
	}
	for _, s := range skips {
		parts = append(parts, fmt.Sprintf("skip %s: %s", s.Container, s.Reason))
	}
	return strings.Join(parts, " ")
}

func TestRoutes(t *testing.T) {
	tests := []struct {
		name string
		in   []Container
		want string // routes, then skips; "" for nothing
	}{
		{"plain container", []Container{ctr("web", nil, pub(8080, 80))}, "web.test->8080(web)"},
		{"compose service", []Container{ctr("shop-api-1", compose("shop", "api"), pub(49153, 3000))}, "api.shop.test->49153(shop-api-1)"},
		{"name is made a hostname", []Container{ctr("My_App", nil, pub(9000, 9000))}, "my-app.test->9000(My_App)"},
		{"IPv4 and IPv6 mapping of one port", []Container{ctr("web", nil, pub(8080, 80), Port{IP: "::", PrivatePort: 80, PublicPort: 8080, Type: "tcp"})}, "web.test->8080(web)"},
		{"unpublished and UDP ports ignored", []Container{ctr("web", nil, Port{PrivatePort: 5432, Type: "tcp"}, Port{IP: "0.0.0.0", PrivatePort: 53, PublicPort: 53, Type: "udp"}, pub(8000, 8000))}, "web.test->8000(web)"},
		{"no published port: silently ignored", []Container{ctr("db", nil, Port{PrivatePort: 5432, Type: "tcp"})}, ""},
		{"disabled by label", []Container{ctr("web", map[string]string{LabelEnable: "false"}, pub(8080, 80))}, ""},
		{"port label: container port", []Container{ctr("app", map[string]string{LabelPort: "9229"}, pub(1234, 3000), pub(5678, 9229))}, "app.test->5678(app)"},
		{"port label: host port also accepted", []Container{ctr("app", map[string]string{LabelPort: "1234"}, pub(1234, 3000), pub(5678, 9229))}, "app.test->1234(app)"},
		{"port label not published", []Container{ctr("app", map[string]string{LabelPort: "4000"}, pub(1234, 3000))}, "skip app: dev.switchboard.port=4000, but container port 4000 isn't published (published: 1234->3000)"},
		{"port label invalid", []Container{ctr("app", map[string]string{LabelPort: "http"}, pub(1234, 3000))}, `skip app: dev.switchboard.port="http" is not a port number`},
		{"several ports: 80 first", []Container{ctr("app", nil, pub(1, 3000), pub(2, 8080), pub(3, 80))}, "app.test->3(app)"},
		{"several ports: then 8080", []Container{ctr("app", nil, pub(1, 3000), pub(2, 8080), pub(3, 5000))}, "app.test->2(app)"},
		{"several ports: then 3000", []Container{ctr("app", nil, pub(1, 3000), pub(3, 5000))}, "app.test->1(app)"},
		{"several ports: ambiguous", []Container{ctr("app", nil, pub(1, 5000), pub(2, 6000))}, "skip app: it publishes 2 ports (1->5000, 2->6000) and none is container port 80, 8080 or 3000; set dev.switchboard.port"},
		{"published only on a LAN address", []Container{ctr("app", nil, Port{IP: "192.168.1.5", PrivatePort: 80, PublicPort: 80, Type: "tcp"})}, "skip app: its ports are published only on 192.168.1.5; publish them on all addresses or 127.0.0.1"},
		{"loopback-only publish is fine", []Container{ctr("app", nil, Port{IP: "127.0.0.1", PrivatePort: 80, PublicPort: 8081, Type: "tcp"})}, "app.test->8081(app)"},
		{"hosts label replaces the default", []Container{ctr("shop-web-1", compose("shop", "web", LabelHosts, "shop, *.shop , admin.shop.test"), pub(8080, 80))},
			"shop.test->8080(shop-web-1) *.shop.test->8080(shop-web-1) admin.shop.test->8080(shop-web-1)"},
		{"bad name in hosts label", []Container{ctr("web", map[string]string{LabelHosts: "ok,not valid!"}, pub(8080, 80))},
			`ok.test->8080(web) skip web: "not valid!" is not a valid hostname; fix dev.switchboard.hosts`},
		{"scaled compose service: first replica wins", []Container{
			ctr("shop-web-2", compose("shop", "web"), pub(2, 80)), ctr("shop-web-1", compose("shop", "web"), pub(1, 80)),
		}, "web.shop.test->1(shop-web-1) skip shop-web-2: web.shop.test is already used by container shop-web-1; set dev.switchboard.hosts on one of them"},
		{"unusable name", []Container{ctr("__", nil, pub(1, 80))}, `skip __: can't make a hostname from "__"; set dev.switchboard.hosts`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := summary(Routes(tt.in, tlds)); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestRoutesRedirectByDefault(t *testing.T) {
	routes, _ := Routes([]Container{ctr("web", nil, pub(8080, 80))}, tlds)
	if len(routes) != 1 || !routes[0].RedirectHTTPS || routes[0].Wildcard {
		t.Errorf("routes = %+v", routes)
	}
}
