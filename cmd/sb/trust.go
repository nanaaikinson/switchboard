package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/dns"
	"github.com/nanaaikinson/switchboard/internal/pki"
	"github.com/nanaaikinson/switchboard/internal/platform"
)

// ensureCA loads the local CA, creating it on first use.
func ensureCA() (*pki.CA, error) {
	dir, err := pki.DefaultDir()
	if err != nil {
		return nil, err
	}
	return pki.LoadOrCreate(dir, dns.DefaultTLDs())
}

// existingCACert returns the CA certificate path, or "" if there is no CA.
func existingCACert() (string, error) {
	dir, err := pki.DefaultDir()
	if err != nil {
		return "", err
	}
	path := pki.CertPath(dir)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return "", nil
	} else if err != nil {
		return "", err
	}
	return path, nil
}

// caSummary describes the CA for confirmation prompts.
func caSummary(ca *pki.CA) string {
	tlds := make([]string, len(ca.Cert.PermittedDNSDomains))
	for i, d := range ca.Cert.PermittedDNSDomains {
		tlds[i] = "." + d
	}
	return fmt.Sprintf("The CA can only sign names under %s. Its SHA-256 fingerprint is %s.",
		strings.Join(tlds, ", "), ca.Fingerprint())
}

// trustNSS adds the CA to the user's NSS stores; failures only warn, since
// the system store is what most browsers use.
func trustNSS(cmd *cobra.Command, p platform.Platform, certPath string) {
	if err := p.TrustNSS(certPath); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: Firefox: %v\n", err)
	}
}

func untrustNSS(cmd *cobra.Command, p platform.Platform, certPath string) {
	if err := p.UntrustNSS(certPath); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: Firefox: %v\n", err)
	}
}

func newTrustCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "trust",
		Short: "Trust Switchboard's local CA for HTTPS (asks for your password once)",
		Long: `Create the local CA if needed, then trust it so browsers accept Switchboard's
HTTPS certificates without warnings. sb trust prints every change and asks
for confirmation, then runs a single 'sudo sb helper trust' for the system
trust store (macOS: System keychain; Linux: the distro CA bundle). It then
adds the CA to your browsers' NSS stores as you, not root.

The CA is name-constrained: it can only sign names under Switchboard's TLDs,
never real domains or IP addresses. 'sb setup' already does this; run
sb trust again after 'sb untrust' or after installing a browser.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() == 0 {
				return errors.New("run 'sb trust' as your normal user, not with sudo; it asks for your password once")
			}
			p, opts := currentPlatform()
			ca, err := ensureCA()
			if err != nil {
				return fmt.Errorf("trust: %w", err)
			}
			plan := p.TrustPlan(ca.CertPath())
			if len(plan) == 0 {
				return fmt.Errorf("trusting the CA is not implemented on %s yet; add %s to your trust store by hand", runtime.GOOS, ca.CertPath())
			}
			argv := []string{opts.SbPath, "helper", "trust", "--ca-cert", ca.CertPath()}
			ok, err := confirm(cmd, "sb trust will:", plan, argv, yes, caSummary(ca)+"\nUndo with 'sb untrust'.")
			if err != nil || !ok {
				return err
			}
			if err := runSudo(cmd.Context(), argv, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return fmt.Errorf("trust failed: %w; nothing else was changed, so it is safe to run 'sb trust' again", err)
			}
			trustNSS(cmd, p, ca.CertPath())
			fmt.Fprintln(cmd.OutOrStdout(), "The Switchboard CA is trusted. Restart Firefox if it is open.")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newUntrustCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "untrust",
		Short: "Stop trusting Switchboard's local CA",
		Long: `Remove the local CA from the system trust store (with a single
'sudo sb helper untrust') and from your Firefox stores. The CA files in the
config dir are kept, so 'sb trust' can trust the same CA again. HTTPS names
show certificate warnings until then.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() == 0 {
				return errors.New("run 'sb untrust' as your normal user, not with sudo; it asks for your password once")
			}
			p, opts := currentPlatform()
			path, err := existingCACert()
			if err != nil {
				return fmt.Errorf("untrust: %w", err)
			}
			if path == "" {
				fmt.Fprintln(cmd.OutOrStdout(), "There is no Switchboard CA, so nothing is trusted.")
				return nil
			}
			plan := p.UntrustPlan(path)
			if len(plan) == 0 {
				return fmt.Errorf("untrusting the CA is not implemented on %s yet; remove %s from your trust store by hand", runtime.GOOS, path)
			}
			argv := []string{opts.SbPath, "helper", "untrust", "--ca-cert", path}
			ok, err := confirm(cmd, "sb untrust will:", plan, argv, yes, "Trust it again later with 'sb trust'.")
			if err != nil || !ok {
				return err
			}
			if err := runSudo(cmd.Context(), argv, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return fmt.Errorf("untrust failed: %w; it is safe to run 'sb untrust' again", err)
			}
			untrustNSS(cmd, p, path)
			fmt.Fprintln(cmd.OutOrStdout(), "The Switchboard CA is no longer trusted.")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}
