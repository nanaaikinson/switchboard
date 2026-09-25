package docker

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/nanaaikinson/switchboard/internal/config"
)

// Container labels Switchboard reads.
const (
	LabelEnable = "dev.switchboard.enable" // "false" skips the container
	LabelHosts  = "dev.switchboard.hosts"  // comma list of names, replacing the default
	LabelPort   = "dev.switchboard.port"   // container port to route to

	labelComposeProject = "com.docker.compose.project"
	labelComposeService = "com.docker.compose.service"
)

// preferredPorts are tried, in order, when a container publishes several.
var preferredPorts = []int{80, 8080, 3000}

// Container is the part of a GET /containers/json entry Switchboard uses.
type Container struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
	Ports  []Port            `json:"Ports"`
}

// Port is one port of a container; PublicPort is 0 when it isn't published.
type Port struct {
	IP          string `json:"IP"`
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

// Name is the container's name without Docker's leading slash.
func (c Container) Name() string {
	if len(c.Names) == 0 {
		return c.ID
	}
	return strings.TrimPrefix(c.Names[0], "/")
}

// Route is a route for a running container.
type Route struct {
	config.Route
	Container string
}

// Skip is a container that publishes ports but gets no route, and why.
type Skip struct {
	Container string
	Reason    string
}

// Routes computes the routes for containers, in a stable order, and the
// containers (or names) that were skipped. A container gets routes only if it
// publishes a TCP port on an address the proxy can reach.
func Routes(containers []Container, tlds []string) ([]Route, []Skip) {
	cs := slices.Clone(containers)
	slices.SortFunc(cs, func(a, b Container) int { return strings.Compare(a.Name(), b.Name()) })

	var routes []Route
	var skips []Skip
	taken := map[string]string{} // route name -> container
	for _, c := range cs {
		name := c.Name()
		if strings.EqualFold(strings.TrimSpace(c.Labels[LabelEnable]), "false") {
			continue
		}
		published, elsewhere := publishedPorts(c)
		if len(published) == 0 {
			switch {
			case elsewhere != "":
				skips = append(skips, Skip{name, fmt.Sprintf("its ports are published only on %s; publish them on all addresses or 127.0.0.1", elsewhere)})
			case c.Labels[LabelPort] != "" || c.Labels[LabelHosts] != "":
				skips = append(skips, Skip{name, "it publishes no TCP port; add one, e.g. ports: [\"3000:3000\"]"})
			}
			continue // containers without published ports are not web apps to route
		}
		port, why := choosePort(c.Labels[LabelPort], published)
		if why != "" {
			skips = append(skips, Skip{name, why})
			continue
		}
		hosts, why := hostNames(c)
		if why != "" {
			skips = append(skips, Skip{name, why})
			continue
		}
		for _, h := range hosts {
			q := config.QualifyName(h, tlds)
			switch {
			case !config.ValidHostname(q):
				skips = append(skips, Skip{name, fmt.Sprintf("%q is not a valid hostname; fix %s", h, LabelHosts)})
			case taken[q] != "":
				skips = append(skips, Skip{name, fmt.Sprintf("%s is already used by container %s; set %s on one of them", q, taken[q], LabelHosts)})
			default:
				taken[q] = name
				routes = append(routes, Route{Route: config.Route{Name: q, Port: port, RedirectHTTPS: true}, Container: name})
			}
		}
	}
	return routes, skips
}

// publishedPorts returns the container's TCP ports published on an address
// the proxy reaches through 127.0.0.1, one entry per host port. elsewhere
// names an address ports were published on instead, if any.
func publishedPorts(c Container) (ports []Port, elsewhere string) {
	for _, p := range c.Ports {
		if p.PublicPort == 0 || !strings.EqualFold(p.Type, "tcp") {
			continue
		}
		switch p.IP {
		case "", "0.0.0.0", "::", "127.0.0.1":
		default:
			elsewhere = p.IP
			continue
		}
		if !slices.ContainsFunc(ports, func(q Port) bool { return q.PublicPort == p.PublicPort }) {
			ports = append(ports, p)
		}
	}
	return ports, elsewhere
}

// choosePort picks the host port to route to: the one for the container port
// in the label, else the only one, else the one for container port 80, 8080
// or 3000, in that order.
func choosePort(label string, published []Port) (int, string) {
	if label = strings.TrimSpace(label); label != "" {
		want, err := strconv.Atoi(label)
		if err != nil || want < 1 || want > 65535 {
			return 0, fmt.Sprintf("%s=%q is not a port number", LabelPort, label)
		}
		for _, p := range published {
			if p.PrivatePort == want {
				return p.PublicPort, ""
			}
		}
		for _, p := range published {
			if p.PublicPort == want { // also accept the host port
				return p.PublicPort, ""
			}
		}
		return 0, fmt.Sprintf("%s=%d, but container port %d isn't published (published: %s)", LabelPort, want, want, describe(published))
	}
	if len(published) == 1 {
		return published[0].PublicPort, ""
	}
	for _, want := range preferredPorts {
		for _, p := range published {
			if p.PrivatePort == want {
				return p.PublicPort, ""
			}
		}
	}
	return 0, fmt.Sprintf("it publishes %d ports (%s) and none is container port 80, 8080 or 3000; set %s", len(published), describe(published), LabelPort)
}

func describe(ports []Port) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = fmt.Sprintf("%d->%d", p.PublicPort, p.PrivatePort)
	}
	return strings.Join(s, ", ")
}

// hostNames returns the names from the hosts label, else
// <service>.<project> for Compose containers, else the container name.
func hostNames(c Container) ([]string, string) {
	if list := c.Labels[LabelHosts]; strings.TrimSpace(list) != "" {
		var out []string
		for _, h := range strings.Split(list, ",") {
			if h = strings.ToLower(strings.TrimSpace(h)); h != "" {
				out = append(out, h)
			}
		}
		return out, ""
	}
	var name string
	if svc, proj := c.Labels[labelComposeService], c.Labels[labelComposeProject]; svc != "" && proj != "" {
		name = hostLabel(svc) + "." + hostLabel(proj)
	} else {
		name = hostLabel(c.Name())
	}
	if !config.ValidHostname(name) {
		return nil, fmt.Sprintf("can't make a hostname from %q; set %s", c.Name(), LabelHosts)
	}
	return []string{name}, ""
}

// hostLabel turns a container, service or project name into hostname
// labels: lowercase, with anything but letters, digits, dots and hyphens
// replaced by hyphens.
func hostLabel(s string) string {
	b := []byte(strings.ToLower(s))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' {
			b[i] = '-'
		}
	}
	return strings.Trim(string(b), "-.")
}
