package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/dns"
	"github.com/nanaaikinson/switchboard/internal/platform"
)

// runSudo runs `sudo args...` attached to the terminal. Swapped in tests so
// they never invoke sudo.
var runSudo = func(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, "sudo", args...) //nolint:gosec // G204: argv built from our own binary path and validated flags
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	return cmd.Run()
}

func defaultTLD() string { return dns.DefaultTLDs()[0] }

func defaultDNSPort() int {
	_, port, _ := net.SplitHostPort(dns.DefaultAddr)
	n, _ := strconv.Atoi(port)
	return n
}

func newSetupCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "One-time system setup (asks for your password once)",
		Long: `Configure the system so Switchboard names work. sb setup prints every change,
asks for confirmation, then runs a single 'sudo sb helper install' that:

  - writes /etc/resolver/<tld> so the OS sends .<tld> lookups to Switchboard
  - installs a root LaunchDaemon (the helper) that binds port 80 and hands
    it to your daemon
  - installs a LaunchAgent that runs 'sb daemon' as you at login

Run it as your normal user, not with sudo. Undo everything with 'sb uninstall'.
macOS only for now.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() == 0 {
				return errors.New("run 'sb setup' as your normal user, not with sudo; it asks for your password once")
			}
			p, opts := currentPlatform()
			if err := p.Validate(); err != nil {
				return fmt.Errorf("setup: %w", err)
			}
			tld, port := defaultTLD(), defaultDNSPort()
			argv := []string{opts.SbPath, "helper", "install",
				"--uid", strconv.Itoa(opts.UID), "--home", opts.Home, "--sb-path", opts.SbPath,
				"--tld", tld, "--dns-port", strconv.Itoa(port)}
			ok, err := confirm(cmd, "sb setup will make these system changes:", p.InstallPlan(tld, port), argv, yes,
				"Undo everything later with 'sb uninstall'.")
			if err != nil || !ok {
				return err
			}
			if err := runSudo(cmd.Context(), argv, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return fmt.Errorf("setup failed: %w; run 'sb uninstall' to remove anything that was installed", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Setup complete. Try: sb add myapp 3000 && sb open myapp\n")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func newUninstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove every system change made by 'sb setup'",
		Long: `Reverse every step of 'sb setup' with a single 'sudo sb helper uninstall':
unload and remove the LaunchAgent and helper LaunchDaemon, the helper binary,
its logs and socket, and /etc/resolver/<tld> if Switchboard wrote it.

Safe to run more than once; anything already gone is skipped. Your routes in
the config dir are kept; delete that folder to remove them too.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() == 0 {
				return errors.New("run 'sb uninstall' as your normal user, not with sudo; it asks for your password once")
			}
			p, opts := currentPlatform()
			tld := defaultTLD()
			plan := p.UninstallPlan(tld)
			if len(plan) == 0 {
				return fmt.Errorf("uninstall: %w", p.Validate())
			}
			argv := []string{opts.SbPath, "helper", "uninstall",
				"--uid", strconv.Itoa(opts.UID), "--home", opts.Home, "--tld", tld}
			ok, err := confirm(cmd, "sb uninstall will remove:", plan, argv, yes,
				"Your routes in the config dir are kept.")
			if err != nil || !ok {
				return err
			}
			if err := runSudo(cmd.Context(), argv, cmd.InOrStdin(), cmd.OutOrStdout(), cmd.ErrOrStderr()); err != nil {
				return fmt.Errorf("uninstall failed: %w; it is safe to run 'sb uninstall' again", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Switchboard system changes removed.")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// currentPlatform is swapped in tests.
var currentPlatform = func() (platform.Platform, platform.Options) {
	o := platform.Options{UID: os.Getuid()}
	o.Home, _ = os.UserHomeDir()
	o.SbPath, _ = os.Executable()
	return platform.New(o), o
}

// confirm prints the plan and the exact sudo command, then asks y/N.
func confirm(cmd *cobra.Command, title string, plan, argv []string, yes bool, footer string) (bool, error) {
	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "%s\n\n", title)
	for i, line := range plan {
		fmt.Fprintf(out, "  %d. %s\n", i+1, line)
	}
	fmt.Fprintf(out, "\nIt runs this one command with sudo (you may be asked for your password):\n\n  sudo %s\n\n%s\n", shellJoin(argv), footer)
	if yes {
		return true, nil
	}
	fmt.Fprint(out, "Continue? [y/N] ")
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
		fmt.Fprintln(out, "Aborted; nothing was changed.")
		return false, nil
	}
	return true, nil
}

func shellJoin(argv []string) string {
	q := make([]string, len(argv))
	for i, a := range argv {
		if a != "" && strings.IndexFunc(a, func(r rune) bool { return !isSafeShellChar(r) }) < 0 {
			q[i] = a
		} else {
			q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(q, " ")
}

func isSafeShellChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("/-._:", r)
}

func newHelperCmd() *cobra.Command {
	helper := &cobra.Command{
		Use:   "helper",
		Short: "Privileged helper (internal; run by sb setup and launchd)",
		Long: `The privileged helper. It only binds port 80 and passes it to your daemon,
writes/removes split-DNS config, and installs/removes the launchd jobs.
Run via 'sb setup' and 'sb uninstall', never by hand.`,
		Hidden: true,
	}
	var o platform.Options
	var tld string
	var dnsPort int
	requireRoot := func(*cobra.Command, []string) error {
		if os.Geteuid() != 0 {
			return errors.New("sb helper must run as root; use 'sb setup' or 'sb uninstall' instead")
		}
		return nil
	}

	install := &cobra.Command{
		Use: "install", Short: "Install system changes (root)", Args: cobra.NoArgs, PreRunE: requireRoot,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := platform.New(o)
			if err := p.Validate(); err != nil {
				return err
			}
			if err := p.InstallResolver(tld, dnsPort); err != nil {
				return err
			}
			if err := p.InstallService(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Installed resolver, helper and daemon.")
			return nil
		},
	}
	uninstall := &cobra.Command{
		Use: "uninstall", Short: "Remove system changes (root)", Args: cobra.NoArgs, PreRunE: requireRoot,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.UID <= 0 {
				return errors.New("--uid must be a regular user")
			}
			p := platform.New(o)
			errs := []error{p.RemoveService(), p.RemoveResolver(tld)}
			for _, err := range errs {
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
				}
			}
			return errors.Join(errs...)
		},
	}
	serve := &cobra.Command{
		Use: "serve", Short: "Serve the listener protocol (root, launchd)", Args: cobra.NoArgs, PreRunE: requireRoot,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.UID <= 0 {
				return errors.New("--uid must be a regular user")
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return platform.New(o).ServeHelper(ctx)
		},
	}
	for _, c := range []*cobra.Command{install, uninstall, serve} {
		c.Flags().IntVar(&o.UID, "uid", 0, "uid of the user Switchboard is installed for")
		_ = c.MarkFlagRequired("uid")
	}
	for _, c := range []*cobra.Command{install, uninstall} {
		c.Flags().StringVar(&o.Home, "home", "", "that user's home directory")
		c.Flags().StringVar(&tld, "tld", defaultTLD(), "TLD to configure")
		_ = c.MarkFlagRequired("home")
	}
	install.Flags().StringVar(&o.SbPath, "sb-path", "", "absolute path of the sb binary")
	install.Flags().IntVar(&dnsPort, "dns-port", defaultDNSPort(), "port of the daemon's DNS server")
	_ = install.MarkFlagRequired("sb-path")
	helper.AddCommand(install, uninstall, serve)
	return helper
}
