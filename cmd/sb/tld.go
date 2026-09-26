package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/config"
)

func newTLDCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tld",
		Short: "List and manage TLDs, including the experimental .local mode",
		Long: `Switchboard serves .test through split DNS, set up by 'sb setup'.

.local mode (EXPERIMENTAL) is opt-in: 'sb tld add local --mdns' makes the
daemon announce every route under .local over multicast DNS (through
mDNSResponder on macOS, Avahi on Linux, a built-in responder elsewhere),
pointing at 127.0.0.1 and ::1, on the loopback interface only. Names are announced when routes are
added, and withdrawn when they are removed or the daemon stops; nothing on
the system is changed.

Limits of .local mode:
  - Wildcard routes (*.myapp.local) can't be announced over mDNS; add each
    name you need. A route's --wildcard subdomains aren't announced either.
  - Some networks answer .local through unicast DNS, which can send apps to
    the wrong host; 'sb doctor' checks for this.
  - HTTPS needs a CA that covers .local; 'sb tld add' says if yours doesn't.`,
	}
	cmd.AddCommand(newTLDLsCmd(), newTLDAddCmd(), newTLDRmCmd())
	return cmd
}

func newTLDLsCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List the TLDs Switchboard serves",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			tlds, err := c.TLDs(cmd.Context())
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if asJSON {
				enc := json.NewEncoder(out)
				enc.SetIndent("", "  ")
				return enc.Encode(tlds)
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "TLD\tRESOLVED BY\tNOTE")
			for _, t := range tlds {
				by, note := "split DNS", ""
				if t.Default {
					note = "default"
				}
				if t.MDNS {
					by, note = "mDNS", "experimental; no wildcards"
				}
				fmt.Fprintf(tw, ".%s\t%s\t%s\n", t.Name, by, note)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print TLDs as JSON")
	return cmd
}

func newTLDAddCmd() *cobra.Command {
	var useMDNS bool
	cmd := &cobra.Command{
		Use:   "add <tld> --mdns",
		Short: "Turn on the experimental .local mode",
		Long: `Add a TLD. Only 'sb tld add local --mdns' is supported: routes under .local
are then announced over multicast DNS (EXPERIMENTAL). See 'sb tld --help'.`,
		Example: "  sb tld add local --mdns\n  sb add myapp.local 3000",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			created, err := c.AddTLD(cmd.Context(), args[0], useMDNS)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if !created {
				fmt.Fprintf(out, ".%s is already served.\n", strings.Trim(strings.ToLower(args[0]), "."))
				return nil
			}
			fmt.Fprintf(out, `Added .%[1]s (mDNS, EXPERIMENTAL). Routes under .%[1]s are announced on the
loopback interface only, for example:
  sb add myapp.%[1]s 3000
Wildcard routes can't be announced over mDNS; add each name you need.
Run 'sb doctor' to check that .%[1]s lookups stay on this machine.
`, config.MDNSTLD)
			if ca, err := c.CA(cmd.Context()); err == nil && ca.Present && !slices.Contains(ca.TLDs, config.MDNSTLD) {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: the local CA can't sign .%s names, so HTTPS for them fails until you make a new CA: %s\n",
					config.MDNSTLD, rotateCAHint())
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&useMDNS, "mdns", false, "resolve names under the TLD over multicast DNS (experimental; .local only)")
	return cmd
}

func newTLDRmCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "rm <tld>",
		Short:   "Turn off .local mode",
		Long:    "Remove an opt-in TLD. Remove its routes first; their names are withdrawn from mDNS.",
		Example: "  sb tld rm local",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			if err := c.RemoveTLD(cmd.Context(), args[0]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed .%s\n", args[0])
			return nil
		},
	}
}
