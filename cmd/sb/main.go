// Command sb is the Switchboard binary. It runs as the background daemon
// (sb daemon), the privileged helper (sb helper), or the CLI (sb <command>).
package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

var errNotImplemented = errors.New("not implemented yet")

func main() {
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "sb: %v\n", err)
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "sb",
		Short: "Map local ports to trusted HTTPS names",
		Long: `Switchboard maps local ports to trusted HTTPS names, for example
localhost:7000 -> https://myapp.test, with subdomains and wildcards.

Run 'sb setup' once, then 'sb add <name> <port>' for each app.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		// Modes.
		stub(&cobra.Command{
			Use:   "daemon",
			Short: "Run the Switchboard daemon (DNS, proxy, control API)",
			Long: `Run the background daemon that holds the route table and serves DNS,
the reverse proxy and the control API. Normally started by the OS service
manager, not by hand.`,
			Args: cobra.NoArgs,
		}),
		stub(&cobra.Command{
			Use:   "helper",
			Short: "Run the privileged helper (internal)",
			Long: `Run the privileged helper. It binds ports 80/443, writes split-DNS config
and manages the local CA in trust stores. It is started by the OS service
manager and accepts commands only from the daemon; do not run it by hand.`,
			Args:   cobra.NoArgs,
			Hidden: true,
		}),
		// CLI.
		stub(&cobra.Command{
			Use:     "add <name> <port>",
			Short:   "Route a name to a local port",
			Example: "  sb add myapp 7000\n  sb add api.myapp 7001\n  sb add '*.tenants.myapp' 3000",
			Args:    cobra.ExactArgs(2),
		}),
		stub(&cobra.Command{
			Use:     "rm <name>",
			Short:   "Remove a route",
			Example: "  sb rm api.myapp",
			Args:    cobra.ExactArgs(1),
		}),
		stub(&cobra.Command{
			Use:   "ls",
			Short: "List routes and their status",
			Args:  cobra.NoArgs,
		}),
		stub(&cobra.Command{
			Use:     "open <name>",
			Short:   "Open a route in the default browser",
			Example: "  sb open myapp",
			Args:    cobra.ExactArgs(1),
		}),
		stub(&cobra.Command{
			Use:   "doctor",
			Short: "Diagnose DNS, ports, certificates and conflicting tools",
			Args:  cobra.NoArgs,
		}),
		stub(&cobra.Command{
			Use:   "setup",
			Short: "One-time system setup (requires admin rights)",
			Long: `Install the privileged helper, register split DNS for the Switchboard TLD
and trust the local CA. Asks for admin rights once. Undo with 'sb uninstall'.`,
			Args: cobra.NoArgs,
		}),
		stub(&cobra.Command{
			Use:   "uninstall",
			Short: "Remove every system change made by Switchboard",
			Long: `Remove the helper, split-DNS config, local CA and background services
installed by 'sb setup', leaving the system as it was.`,
			Args: cobra.NoArgs,
		}),
	)
	return root
}

// stub gives cmd a RunE that reports the command is not implemented.
func stub(cmd *cobra.Command) *cobra.Command {
	cmd.RunE = func(c *cobra.Command, _ []string) error {
		return fmt.Errorf("%s: %w", c.Name(), errNotImplemented)
	}
	return cmd
}
