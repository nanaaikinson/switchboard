package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/dashboard"
)

func newDashboardCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Open the web dashboard in your browser",
		Long: `Open the web dashboard at https://switchboard.<tld>. It shows routes and their
status live, lets you add, change and delete routes, shows each route's recent
requests, and whether the local CA is trusted.

sb dashboard signs you in with a link that works once, for two minutes; the
browser then keeps a session cookie until the daemon restarts. Opening the
dashboard any other way asks you to run sb dashboard. With --print, it prints
the link instead of opening it.`,
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
			if !st.HTTPS.Listening {
				return fmt.Errorf("the dashboard needs HTTPS, which isn't running (%s); run 'sb doctor'", st.HTTPS.Error)
			}
			tok, err := c.DashboardLogin(cmd.Context())
			if err != nil {
				return err
			}
			base := "https://" + dashboard.Host(st.TLDs[0]) + portSuffix(st.HTTPS.Addrs, "443")
			link := base + "/login?token=" + url.QueryEscape(tok)
			if printOnly {
				fmt.Fprintln(cmd.OutOrStdout(), link)
				fmt.Fprintln(cmd.ErrOrStderr(), "The link signs you in once, within two minutes. Don't share it.")
				return nil
			}
			if err := openURL(link); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Opened %s/\n", base)
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the sign-in link instead of opening it")
	return cmd
}
