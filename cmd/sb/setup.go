package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"runtime"
	"slices"
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

// defaultDNSPort is where 'sb setup' points split DNS.
func defaultDNSPort() int {
	_, port, _ := net.SplitHostPort(dns.ResolverAddr)
	n, _ := strconv.Atoi(port)
	return n
}

func newSetupCmd() *cobra.Command {
	var f privFlags
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "One-time system setup (asks for your password once)",
		Long: `Configure the system so Switchboard names work. sb setup prints every change,
asks for confirmation, then runs a single 'sudo sb helper install' (on
Windows: one UAC prompt) that:

  - sends .<tld> lookups to Switchboard's DNS server
      macOS:   /etc/resolver/<tld>
      Linux:   a systemd-resolved or NetworkManager dnsmasq drop-in, or, if
               neither is in use, a block in /etc/hosts (exact names only;
               wildcard routes don't resolve)
      Windows: an NRPT rule for .<tld> pointing at 127.0.0.1
  - installs the privileged helper as a root service (a launchd daemon or a
    systemd unit) that binds ports 80 and 443, and the DNS port split DNS
    points at, and hands them to your daemon. Windows needs no helper: the
    daemon binds 53, 80 and 443 itself
  - installs a per-user service (a LaunchAgent, a systemd user unit, or a
    Scheduled Task at logon on Windows) that runs 'sb daemon' as you
  - trusts Switchboard's local CA (created now if needed) in the system trust
    store (LocalMachine\Root on Windows), so HTTPS names have no certificate
    warnings

It then adds the CA to your browsers' NSS stores (Firefox; Chrome on Linux)
as you, not root.

Run it as your normal user, not with sudo. Undo everything with 'sb uninstall'.
Supports macOS, Linux (systemd) and Windows 10 1809+ / 11.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if os.Geteuid() == 0 {
				return errors.New("run 'sb setup' as your normal user, not with sudo; it asks for your password once")
			}
			p, opts := currentPlatform()
			if err := p.Validate(); err != nil {
				return fmt.Errorf("setup: %w", err)
			}
			target, err := caToTrust()
			if err != nil {
				return fmt.Errorf("setup: %w", err)
			}
			tld, port := defaultTLD(), defaultDNSPort()
			argv := append([]string{opts.SbPath, "helper", "install", "--home", opts.Home, "--sb-path", opts.SbPath,
				"--tld", tld, "--dns-port", strconv.Itoa(port)}, target.helperArgs()...)
			argv = append(argv, helperUserArgs(opts)...)
			ran, err := runPrivileged(cmd, p, f, privPlan{
				Title:   "sb setup will make these system changes:",
				Changes: append(p.InstallPlan(tld, port), p.TrustPlan(target.ca.CertPath())...),
				Command: argv,
				Notes:   target.notes() + "\nUndo everything later with 'sb uninstall'.",
				Prompt:  "Switchboard wants to set up ." + tld + " names, trusted HTTPS and its background services.",
			})
			if err != nil {
				return fmt.Errorf("setup failed: %w; run 'sb uninstall' to remove anything that was installed", err)
			}
			if !ran {
				return nil
			}
			if err := target.finish(cmd, p); err != nil {
				return fmt.Errorf("setup: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Setup complete. Try: sb add myapp 3000 && sb open myapp\n")
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

func newUninstallCmd() *cobra.Command {
	var f privFlags
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove every system change made by 'sb setup'",
		Long: `Reverse every step of 'sb setup' with a single 'sudo sb helper uninstall':
stop and remove the daemon's user service and the helper's root service, the
helper binary, its logs and socket, the split-DNS config if Switchboard wrote
it, and the local CA from the system trust store. Then remove the CA from
your browsers' NSS stores, as you. On Windows (one UAC prompt): the logon
task, Switchboard's NRPT rule and the CA in LocalMachine\Root; rules and
tasks made by other tools are left alone.

Safe to run more than once; anything already gone is skipped. Your routes and
the CA files in the config dir are kept; delete that folder to remove them too.`,
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
			argv := append([]string{opts.SbPath, "helper", "uninstall", "--home", opts.Home, "--tld", tld}, helperUserArgs(opts)...)
			caCert, err := existingCACert()
			if err != nil {
				return fmt.Errorf("uninstall: %w", err)
			}
			// The helper removes the user's CAs from the system store even
			// when the CA files are gone.
			plan = append(plan, p.UntrustPlan(caCert)...)
			if caCert != "" {
				argv = append(argv, "--ca-cert", caCert)
			}
			ran, err := runPrivileged(cmd, p, f, privPlan{
				Title: "sb uninstall will remove:", Changes: plan, Command: argv,
				Notes:  "Your routes and the CA files in the config dir are kept.",
				Prompt: "Switchboard wants to remove its system changes.",
			})
			if err != nil {
				return fmt.Errorf("uninstall failed: %w; it is safe to run 'sb uninstall' again", err)
			}
			if !ran {
				return nil
			}
			if caCert != "" {
				untrustNSS(cmd, p, caCert)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Switchboard system changes removed.")
			return nil
		},
	}
	f.register(cmd)
	return cmd
}

// currentPlatform is swapped in tests.
var currentPlatform = func() (platform.Platform, platform.Options) {
	o := platform.Options{UID: os.Getuid()}
	if u, err := user.Current(); err == nil {
		o.User = u.Username
		if runtime.GOOS == "windows" {
			o.SID = u.Uid
		}
	}
	o.Home, _ = os.UserHomeDir()
	o.SbPath, _ = os.Executable()
	return platform.New(o), o
}

// privFlags are the flags of commands that make one privileged change.
type privFlags struct{ yes, dialog, printPlan bool }

func (f *privFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&f.yes, "yes", "y", false, "do not ask for confirmation")
	cmd.Flags().BoolVar(&f.dialog, "admin-dialog", false,
		"ask for the administrator password in a system dialog instead of sudo in the terminal (for apps, such as the tray app)")
	cmd.Flags().BoolVar(&f.printPlan, "print-plan", false, "print what would change, as JSON, and change nothing")
}

// privPlan is what a privileged command will do.
type privPlan struct {
	Title   string   `json:"title"`
	Changes []string `json:"changes"`
	Command []string `json:"command"` // run as root
	Notes   string   `json:"notes"`
	Prompt  string   `json:"-"` // shown in the admin dialog
}

// runAdmin runs a command from Platform.AdminCommand. Swapped in tests so
// they never raise a password dialog.
var runAdmin = func(c *exec.Cmd) error { return c.Run() }

// runPrivileged shows pl, asks for confirmation unless --yes, then runs
// pl.Command as root: with sudo, or behind the OS password dialog with
// --admin-dialog. With --print-plan it only prints pl as JSON. It reports
// whether the command ran.
func runPrivileged(cmd *cobra.Command, p platform.Platform, f privFlags, pl privPlan) (bool, error) {
	out := cmd.OutOrStdout()
	if f.printPlan {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return false, enc.Encode(pl)
	}
	fmt.Fprintf(out, "%s\n\n", pl.Title)
	for i, line := range pl.Changes {
		fmt.Fprintf(out, "  %d. %s\n", i+1, line)
	}
	dialog := f.dialog || runtime.GOOS == "windows" // Windows has no sudo; UAC asks
	if dialog {
		fmt.Fprintf(out, "\nIt runs this one command as administrator; your system asks for your password:\n\n  %s\n\n%s\n", shellJoin(pl.Command), pl.Notes)
	} else {
		fmt.Fprintf(out, "\nIt runs this one command with sudo (you may be asked for your password):\n\n  sudo %s\n\n%s\n", shellJoin(pl.Command), pl.Notes)
	}
	if !f.yes {
		fmt.Fprint(out, "Continue? [y/N] ")
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			fmt.Fprintln(out, "Aborted; nothing was changed.")
			return false, nil
		}
	}
	if !dialog {
		return true, runSudo(cmd.Context(), pl.Command, cmd.InOrStdin(), out, cmd.ErrOrStderr())
	}
	argv := pl.Command
	var logPath string
	if runtime.GOOS == "windows" {
		// UAC starts the elevated process in its own hidden console, so it
		// writes what it did to a file, shown here afterwards.
		logFile, err := os.CreateTemp("", "sb-helper-*.log")
		if err != nil {
			return false, err
		}
		logPath = logFile.Name()
		_ = logFile.Close()
		defer func() { _ = os.Remove(logPath) }()
		argv = append(slices.Clone(argv), "--log", logPath)
	}
	c, err := p.AdminCommand(argv, pl.Prompt)
	if err != nil {
		return false, err
	}
	c.Stdout, c.Stderr = out, cmd.ErrOrStderr()
	runErr := runAdmin(c)
	if logPath != "" {
		if b, err := os.ReadFile(logPath); err == nil && len(b) > 0 { //nolint:gosec // G304: our temp file
			_, _ = out.Write(b)
		}
	}
	if runErr != nil {
		return false, fmt.Errorf("%w (if you cancelled the password dialog, nothing was changed)", runErr)
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
		Short: "Privileged helper (internal; run by sb setup and the service manager)",
		Long: `The privileged helper. It only binds ports 80 and 443 and the DNS port and
passes them to your daemon, writes/removes split-DNS config (including the /etc/hosts block
on Linux systems without split DNS), installs/removes its services, and
adds/removes the local CA in the system trust store.
Run via 'sb setup', 'sb uninstall', 'sb trust' and 'sb untrust', never by hand.`,
		Hidden: true,
	}
	var o platform.Options
	var tld, caCert, caFingerprint, logPath string
	var dnsPort int
	requireRoot := func(cmd *cobra.Command, _ []string) error {
		if logPath != "" {
			// Elevated on Windows: no console to show; sb setup reads the file.
			// If it isn't the file setup made, carry on without the log.
			if f, err := openHelperLog(logPath); err == nil {
				cmd.SetOut(f)
				cmd.SetErr(f)
			} else {
				cmd.SetOut(io.Discard)
				cmd.SetErr(io.Discard)
			}
		}
		if runtime.GOOS == "windows" {
			if !isAdmin() {
				return errors.New("sb helper must run as administrator; use 'sb setup' or 'sb uninstall' instead")
			}
			if cmd.Flags().Lookup("sid") != nil {
				// The elevated account may not be the user's (over-the-shoulder UAC).
				return checkUserSID(o.User, o.SID)
			}
			return nil
		}
		if os.Geteuid() != 0 {
			return errors.New("sb helper must run as root; use 'sb setup' or 'sb uninstall' instead")
		}
		if cmd.Flags().Lookup("uid") != nil && o.UID <= 0 {
			return errors.New("--uid must be a regular user")
		}
		// Root's temp files go in the sticky /tmp, never a directory the
		// user's environment names, where they could be swapped.
		return os.Setenv("TMPDIR", "/tmp")
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
			if caCert != "" {
				if err := p.TrustCA(caCert, caFingerprint); err != nil {
					return fmt.Errorf("installed resolver, helper and daemon, but %w; run 'sb trust' to retry", err)
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Installed resolver, helper and daemon, and trusted the CA.")
			return nil
		},
	}
	uninstall := &cobra.Command{
		Use: "uninstall", Short: "Remove system changes (root)", Args: cobra.NoArgs, PreRunE: requireRoot,
		RunE: func(cmd *cobra.Command, _ []string) error {
			p := platform.New(o)
			errs := []error{p.RemoveService(), p.RemoveResolver(tld), p.UntrustCA(caCert)}
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
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return platform.New(o).ServeHelper(ctx)
		},
	}
	trust := &cobra.Command{
		Use: "trust", Short: "Trust the local CA in the system store (root)", Args: cobra.NoArgs, PreRunE: requireRoot,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := platform.New(o).TrustCA(caCert, caFingerprint); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Trusted the CA in the system trust store.")
			return nil
		},
	}
	untrust := &cobra.Command{
		Use: "untrust", Short: "Remove the local CA from the system store (root)", Args: cobra.NoArgs, PreRunE: requireRoot,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := platform.New(o).UntrustCA(caCert); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Removed the CA from the system trust store.")
			return nil
		},
	}
	for _, c := range []*cobra.Command{install, uninstall, trust, untrust} {
		c.Flags().StringVar(&caCert, "ca-cert", "", "absolute path of the local CA certificate")
		c.Flags().StringVar(&logPath, "log", "", "write output to this file (for elevated runs without a console)")
		c.Flags().StringVar(&o.User, "user", "", "login name of that user (DOMAIN\\name on Windows)")
		c.Flags().StringVar(&o.SID, "sid", "", "Windows: SID of that user")
		if runtime.GOOS == "windows" {
			_ = c.MarkFlagRequired("sid")
		}
	}
	for _, c := range []*cobra.Command{install, trust} {
		c.Flags().StringVar(&caFingerprint, "ca-fingerprint", "", "SHA-256 fingerprint of the CA the user confirmed; any other is refused")
	}
	install.MarkFlagsRequiredTogether("ca-cert", "ca-fingerprint")
	for _, c := range []*cobra.Command{trust} {
		_ = c.MarkFlagRequired("ca-cert")
		_ = c.MarkFlagRequired("ca-fingerprint")
	}
	for _, c := range []*cobra.Command{install, uninstall, serve, trust, untrust} {
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
	helper.AddCommand(install, uninstall, serve, trust, untrust)
	return helper
}
