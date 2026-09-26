package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/config"
	"github.com/nanaaikinson/switchboard/internal/update"
)

const releasesURL = "https://github.com/nanaaikinson/switchboard/releases"

// selfPath is the running sb binary, symlinks resolved. Swapped in tests so
// they never replace the test binary.
var selfPath = func() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(exe)
}

// updateHTTP fetches manifests and downloads; nil is the default client.
// Swapped in tests.
var updateHTTP *http.Client

// updateBaseURL is $SB_UPDATE_URL, or the default. It must be https, or http
// to localhost for a local test server.
func updateBaseURL() (string, error) {
	u := os.Getenv("SB_UPDATE_URL")
	if u == "" {
		return update.DefaultBaseURL, nil
	}
	if err := update.CheckURL(u); err != nil {
		return "", fmt.Errorf("SB_UPDATE_URL: %w; fix it, or unset it to use %s", err, update.DefaultBaseURL)
	}
	return u, nil
}

// standalone returns the sb binary to update, or an error saying which
// package manager to use instead.
func standalone() (string, error) {
	exe, err := selfPath()
	if err != nil {
		return "", err
	}
	sys := update.System{Run: func(name string, args ...string) error {
		return exec.Command(name, args...).Run() //nolint:gosec // G204: fixed tool (rpm) and our own path
	}}
	if m := update.DetectManaged(exe, sys); m != nil {
		return "", fmt.Errorf("%s was installed by %s, which keeps it up to date; instead run: %s", exe, m.By, m.Update)
	}
	return exe, nil
}

func newSelfUpdateCmd() *cobra.Command {
	var channel string
	var checkOnly bool
	cmd := &cobra.Command{
		Use:   "self-update",
		Short: "Update sb to the latest release",
		Long: `Check the update channel for a newer release and install it in place of
this binary, keeping the current one as sb.old (undo with 'sb rollback').
Then restart the daemon's service so it runs the new version.

The channel's manifest must be signed with the release key built into sb,
for that channel, and be no older than the newest one this install has
accepted (remembered in the config dir), so an old manifest can't be served
again. The stable channel never offers a pre-release. Every download is
checked against its SHA-256 and its minisign signature before anything is
replaced. A release may roll out gradually: each install has a random ID (in
the config dir, never sent anywhere) that decides when it gets the update.

sb installed with Homebrew, winget, Scoop, a .deb or .rpm, or inside the
Switchboard app isn't updated this way; the error says what to run instead.

SB_UPDATE_URL replaces the update server, e.g. for testing. It must be an
https URL (http only for localhost), and so must every redirect.`,
		Example: "  sb self-update\n  sb self-update --check\n  sb self-update --channel beta",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !slices.Contains(update.Channels, channel) {
				return fmt.Errorf("unknown channel %q; use %s", channel, strings.Join(update.Channels, " or "))
			}
			base, err := updateBaseURL()
			if err != nil {
				return err
			}
			exe, err := standalone()
			if err != nil {
				return err
			}
			dir, err := config.Dir()
			if err != nil {
				return err
			}
			id, err := update.InstallID(dir)
			if err != nil {
				return err
			}
			u := update.Updater{
				BaseURL: base, PublicKey: update.ReleaseKey, Current: version,
				Platform: runtime.GOOS + "-" + runtime.GOARCH, InstallID: id, HTTP: updateHTTP, StateDir: dir,
			}
			ctx := cmd.Context()
			c, err := u.Check(ctx, channel)
			if errors.Is(err, update.ErrNoKey) {
				return fmt.Errorf("%w, so it can't verify updates; download the latest release from %s", err, releasesURL)
			}
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "sb %s; latest on %s: %s\n", version, channel, c.Manifest.Version)
			switch {
			case !c.Newer:
				fmt.Fprintln(out, "Already up to date.")
				return nil
			case !c.InRollout:
				fmt.Fprintf(out, "%s is rolling out to %d%% of installs, and this one isn't included yet. Try again later.\n",
					c.Manifest.Version, c.Manifest.Rollout())
				return nil
			case checkOnly:
				fmt.Fprintln(out, "An update is available. Install it with: sb self-update")
				return nil
			}

			bin, err := u.Download(ctx, c)
			if err != nil {
				return err
			}
			staged, err := update.Stage(exe, bin)
			if err != nil {
				return err
			}
			if err := checkRuns(ctx, staged, c.Manifest.Version); err != nil {
				_ = os.Remove(staged)
				return fmt.Errorf("the downloaded sb doesn't work (%w); nothing was changed", err)
			}
			if err := update.Swap(exe, staged); err != nil {
				_ = os.Remove(staged)
				return err
			}
			fmt.Fprintf(out, "Updated %s from %s to %s. The previous version is %s; undo with: sb rollback\n",
				exe, version, c.Manifest.Version, update.OldPath(exe))
			restartDaemon(cmd)
			if runtime.GOOS != "windows" {
				fmt.Fprintln(out, "The privileged helper runs its own copy of sb, which is not updated: run 'sb setup' to update it too, so the privileged part gets this version's fixes.")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "stable", "update channel: stable or beta")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "only say whether an update is available")
	return cmd
}

func newRollbackCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rollback",
		Short: "Go back to the sb from before the last self-update",
		Long: `Swap sb with sb.old, the binary 'sb self-update' replaced, and restart the
daemon's service. Run it again to go forward again.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			exe, err := standalone()
			if err != nil {
				return err
			}
			if err := update.Rollback(exe); err != nil {
				return err
			}
			v, _ := runVersion(cmd.Context(), exe)
			fmt.Fprintf(cmd.OutOrStdout(), "Rolled back %s to %s. The newer version is now %s; run 'sb rollback' again to go forward.\n",
				exe, orUnknown(v), update.OldPath(exe))
			restartDaemon(cmd)
			return nil
		},
	}
}

func orUnknown(v string) string {
	if v == "" {
		return "the previous version"
	}
	return v
}

// runVersion runs `path --version` and returns the version it prints.
func runVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "--version").Output()
	if err != nil {
		return "", err
	}
	v, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "sb version ")
	if !ok {
		return "", fmt.Errorf("unexpected --version output %q", strings.TrimSpace(string(out)))
	}
	return v, nil
}

// checkRuns makes sure a staged binary starts and is the promised version.
func checkRuns(ctx context.Context, path, want string) error {
	got, err := runVersion(ctx, path)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("it reports version %s, not %s", got, want)
	}
	return nil
}

// restartDaemon restarts the daemon's service so it runs the binary now on
// disk, and says what happened.
func restartDaemon(cmd *cobra.Command) {
	p, _ := currentPlatform()
	restarted, err := p.RestartDaemon()
	switch {
	case err != nil:
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: couldn't restart the daemon (%v); log out and in, or re-run 'sb setup'\n", err)
	case restarted:
		fmt.Fprintln(cmd.OutOrStdout(), "Restarted the daemon.")
	default:
		fmt.Fprintln(cmd.OutOrStdout(), "The daemon isn't installed as a service ('sb setup'); restart it yourself if it's running.")
	}
}
