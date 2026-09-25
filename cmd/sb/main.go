// Command sb is the Switchboard binary. It runs as the background daemon
// (sb daemon), the privileged helper (sb helper), or the CLI (sb <command>).
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is set at build time with -ldflags "-X main.version=v1.2.3".
var version = "dev"

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
		newHelperCmd(),
		newAddCmd(),
		newRmCmd(),
		newLsCmd(),
		newOpenCmd(),
		newApplyCmd(),
		newInitCmd(),
		newDoctorCmd(),
		newSetupCmd(),
		newUninstallCmd(),
		newTrustCmd(),
		newUntrustCmd(),
	)
	return root
}
