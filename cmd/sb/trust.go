package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/config"
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
	return pki.LoadOrCreate(dir, caTLDs())
}

// caTLDs are the TLDs a new CA is limited to: the default ones, plus opt-in
// TLDs such as .local from routes.toml.
func caTLDs() []string {
	tlds := dns.DefaultTLDs()
	if path, err := config.DefaultPath(); err == nil {
		if cfg, err := config.Load(path); err == nil {
			for _, t := range cfg.TLDs {
				tlds = append(tlds, t.Name)
			}
		}
	}
	return tlds
}

// trustTarget is the CA that 'sb trust' and 'sb setup' trust: the current
// one or, if an earlier version made it without limiting it to TLS server
// certificates, a new staged CA that replaces it once trusted.
type trustTarget struct {
	dir string
	ca  *pki.CA
	old *pki.CA // the CA ca replaces, or nil
}

func caToTrust() (trustTarget, error) {
	dir, err := pki.DefaultDir()
	if err != nil {
		return trustTarget{}, err
	}
	ca, err := pki.LoadOrCreate(dir, caTLDs())
	if err != nil || !ca.Legacy {
		return trustTarget{dir: dir, ca: ca}, err
	}
	next, err := pki.Stage(dir, caTLDs())
	if err != nil {
		return trustTarget{}, err
	}
	return trustTarget{dir: dir, ca: next, old: ca}, nil
}

// notes describe the CA for confirmation prompts.
func (t trustTarget) notes() string {
	notes := caSummary(t.ca)
	if t.old != nil {
		notes += "\nIt replaces your current CA (SHA-256 " + t.old.Fingerprint() + "), which an earlier version made without limiting it to TLS server certificates; that one is removed."
	}
	return notes
}

// helperArgs are the helper flags that name the CA to trust.
func (t trustTarget) helperArgs() []string {
	return []string{"--ca-cert", t.ca.CertPath(), "--ca-fingerprint", t.ca.Fingerprint()}
}

// finish runs once the system trusts t.ca: it replaces the old CA's files
// with the new one's and restarts the daemon to sign with it, then trusts
// the CA in the user's NSS stores.
func (t trustTarget) finish(cmd *cobra.Command, p platform.Platform) error {
	if t.old != nil {
		untrustNSS(cmd, p, t.old.CertPath())
		if err := pki.Promote(t.dir); err != nil {
			return fmt.Errorf("%w; run 'sb trust' again", err)
		}
		if _, err := p.RestartDaemon(); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: restart the daemon so it uses the new CA: %v\n", err)
		}
	}
	trustNSS(cmd, p, pki.CertPath(t.dir))
	return nil
}

// helperUserArgs are the helper flags that name the user a privileged change
// is for.
func helperUserArgs(o platform.Options) []string {
	args := []string{"--uid", strconv.Itoa(o.UID), "--user", o.User}
	if o.SID != "" {
		args = append(args, "--sid", o.SID)
	}
	return args
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
	var f privFlags
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
			target, err := caToTrust()
			if err != nil {
				return fmt.Errorf("trust: %w", err)
			}
			plan := p.TrustPlan(target.ca.CertPath())
			if len(plan) == 0 {
				return fmt.Errorf("trusting the CA is not implemented on %s yet; add %s to your trust store by hand", runtime.GOOS, target.ca.CertPath())
			}
			argv := append([]string{opts.SbPath, "helper", "trust"}, target.helperArgs()...)
			argv = append(argv, helperUserArgs(opts)...)
			ran, err := runPrivileged(cmd, p, f, privPlan{
				Title: "sb trust will:", Changes: plan, Command: argv,
				Notes:  target.notes() + "\nUndo with 'sb untrust'.",
				Prompt: "Switchboard wants to trust its local certificate authority for HTTPS.",
			})
			if err != nil {
				return fmt.Errorf("trust failed: %w; nothing else was changed, so it is safe to run 'sb trust' again", err)
			}
			if !ran {
				return nil
			}
			if err := target.finish(cmd, p); err != nil {
				return fmt.Errorf("trust: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "The Switchboard CA is trusted. Restart Firefox if it is open.")
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

func newUntrustCmd() *cobra.Command {
	var f privFlags
	cmd := &cobra.Command{
		Use:   "untrust",
		Short: "Stop trusting Switchboard's local CA",
		Long: `Remove the local CA from the system trust store (with a single
'sudo sb helper untrust') and from your Firefox stores. Any other Switchboard
CA of yours in the system store is removed too, even if its files are gone.
The CA files in the config dir are kept, so 'sb trust' can trust the same CA
again. HTTPS names show certificate warnings until then.`,
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
			plan := p.UntrustPlan(path)
			if len(plan) == 0 {
				return fmt.Errorf("untrusting the CA is not implemented on %s yet; remove %s from your trust store by hand", runtime.GOOS, path)
			}
			argv := []string{opts.SbPath, "helper", "untrust"}
			if path != "" {
				argv = append(argv, "--ca-cert", path)
			}
			argv = append(argv, helperUserArgs(opts)...)
			ran, err := runPrivileged(cmd, p, f, privPlan{
				Title: "sb untrust will:", Changes: plan, Command: argv,
				Notes:  "Trust it again later with 'sb trust'.",
				Prompt: "Switchboard wants to stop trusting its local certificate authority.",
			})
			if err != nil {
				return fmt.Errorf("untrust failed: %w; it is safe to run 'sb untrust' again", err)
			}
			if !ran {
				return nil
			}
			if path != "" {
				untrustNSS(cmd, p, path)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "The Switchboard CA is no longer trusted.")
			return nil
		},
	}
	f.register(cmd)
	return cmd
}
