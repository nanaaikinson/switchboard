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
		newDaemonCmd(),
		stub(&cobra.Command{
			Use:   "helper",
			Short: "Run the privileged helper (internal)",
			Long: `Run the privileged helper. It binds ports 80/443, writes split-DNS config
and manages the local CA in trust stores. It is started by the OS service
manager and accepts commands only from the daemon; do not run it by hand.`,
			Args:   cobra.NoArgs,
			Hidden: true,
		}),
		newAddCmd(),
		newRmCmd(),
		newLsCmd(),
		newOpenCmd(),
		stub(&cobra.Command{
			Use:   "doctor",
			Short: "Diagnose DNS, ports, certificates and conflicting tools",
			Args:  cobra.NoArgs,
		}),
		newSetupCmd(),
		newUninstallCmd(),
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
