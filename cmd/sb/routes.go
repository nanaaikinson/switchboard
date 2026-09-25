package main

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/api/client"
	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/platform"
)

// openURL is swapped in tests.
var openURL = platform.OpenURL

func newClient() (*client.Client, error) {
	sock, err := api.DefaultSocketPath()
	if err != nil {
		return nil, err
	}
	return client.New(sock), nil
}

func newAddCmd() *cobra.Command {
	var wildcard, noRedirect bool
	cmd := &cobra.Command{
		Use:   "add <name> <port>",
		Short: "Route a name to a local port",
		Long: `Route a name to an app on 127.0.0.1:<port>. Names without a Switchboard TLD
get the default TLD appended: 'myapp' becomes myapp.test. Prefix a name with
'*.' for a wildcard, or pass --wildcard to also match every subdomain.
Adding an existing name updates it.`,
		Example: "  sb add myapp 7000\n  sb add api.myapp 7001\n  sb add '*.tenants.myapp' 3000\n  sb add myapp 7000 --wildcard",
		Args:    cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			port, err := strconv.Atoi(args[1])
			if err != nil {
				return fmt.Errorf("port %q is not a number; use e.g. sb add %s 3000", args[1], args[0])
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			rs, created, err := c.Put(cmd.Context(), config.Route{
				Name: args[0], Port: port, Wildcard: wildcard, RedirectHTTPS: !noRedirect,
			})
			if err != nil {
				return err
			}
			verb := "Updated"
			if created {
				verb = "Added"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s -> 127.0.0.1:%d\n", verb, displayName(rs.Route), rs.Port)
			return nil
		},
	}
	cmd.Flags().BoolVar(&wildcard, "wildcard", false, "also route every subdomain of the name")
	cmd.Flags().BoolVar(&noRedirect, "no-redirect", false, "do not redirect HTTP to HTTPS for this route")
	return cmd
}

func newRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <name>",
		Short:   "Remove a route",
		Example: "  sb rm api.myapp",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			gone, err := c.Delete(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %s\n", gone.Name)
			return nil
		},
	}
}

func newLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List routes and their status",
		Long: `List routes with the health of their upstream port (up, down, or unknown
before the first check) and where they come from: routes.toml, or a running
Docker container. Lists containers that publish ports but got no route, and
why. Warns when the DNS server, HTTP proxy or HTTPS proxy is not listening.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			st, err := c.Status(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(st.Routes)
			}
			for _, l := range []struct {
				name string
				l    api.Listener
			}{{"DNS server", st.DNS}, {"Proxy", st.Proxy}, {"HTTPS proxy", st.HTTPS}} {
				if !l.l.Listening {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s is not listening: %s\n", l.name, l.l.Error)
				}
			}
			if len(st.Routes) == 0 {
				fmt.Fprintln(out, "No routes. Add one with: sb add myapp 3000")
			} else {
				tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
				fmt.Fprintln(tw, "NAME\tPORT\tUPSTREAM\tSOURCE")
				for _, r := range st.Routes {
					fmt.Fprintf(tw, "%s\t%d\t%s\t%s\n", displayName(r.Route), r.Port, r.Health, source(r))
				}
				if err := tw.Flush(); err != nil {
					return err
				}
			}
			if len(st.Docker.Skipped) > 0 {
				fmt.Fprintln(out, "\nDocker containers without a route:")
				for _, sk := range st.Docker.Skipped {
					fmt.Fprintf(out, "  %s: %s\n", sk.Container, sk.Reason)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print routes as JSON")
	return cmd
}

func newOpenCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "open <name>",
		Short:   "Open a route in the default browser (HTTPS when available)",
		Example: "  sb open myapp\n  sb open acme.tenants.myapp",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			st, err := c.Status(cmd.Context())
			if err != nil {
				return err
			}
			host := config.QualifyName(args[0], st.TLDs)
			if strings.HasPrefix(host, "*.") {
				return fmt.Errorf("%s is a wildcard; open a specific name, e.g. sb open demo.%s", host, strings.TrimPrefix(args[0], "*."))
			}
			if !routed(host, st.Routes) {
				return fmt.Errorf("no route for %s; add one with: sb add %s <port>", host, args[0])
			}
			u := "http://" + host + portSuffix(st.Proxy.Addrs, "80") + "/"
			if st.HTTPS.Listening {
				u = "https://" + host + portSuffix(st.HTTPS.Addrs, "443") + "/"
			}
			if err := openURL(u); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Opened %s\n", u)
			return nil
		},
	}
}

// source is "config", or "docker (<container>)".
func source(r api.RouteStatus) string {
	if r.Source == api.SourceDocker {
		return "docker (" + r.Container + ")"
	}
	return api.SourceConfig
}

// displayName shows wildcard coverage: "myapp.test (+ *.myapp.test)".
func displayName(r config.Route) string {
	if r.Wildcard && !strings.HasPrefix(r.Name, "*.") {
		return r.Name + " (+ *." + r.Name + ")"
	}
	return r.Name
}

// routed reports whether host matches a route exactly or by wildcard.
func routed(host string, routes []api.RouteStatus) bool {
	for _, r := range routes {
		base, star := strings.CutPrefix(r.Name, "*.")
		if (!star && host == r.Name) || ((star || r.Wildcard) && strings.HasSuffix(host, "."+base)) {
			return true
		}
	}
	return false
}

// portSuffix returns ":<port>" when the first address is not on defaultPort.
func portSuffix(addrs []string, defaultPort string) string {
	if len(addrs) == 0 {
		return ""
	}
	if _, port, err := net.SplitHostPort(addrs[0]); err == nil && port != defaultPort {
		return ":" + port
	}
	return ""
}
