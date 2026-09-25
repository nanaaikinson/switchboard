package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/nanaaikinson/switchboard/internal/config"
)

func newApplyCmd() *cobra.Command {
	var down bool
	cmd := &cobra.Command{
		Use:   "apply [path]",
		Short: "Add or update the routes in a project's switchboard.toml",
		Long: `Make the daemon's routes from a switchboard.toml match the file: add new
routes, update changed ones and remove ones no longer listed. Routes from
other sources are never touched. A name that 'sb add', another project file
or a Docker container already has is skipped with a warning. Running it again
without editing the file changes nothing.

Without a path, sb apply uses the nearest switchboard.toml in the current
directory or its parents, up to the git repository root. A path may be the
file or its directory. With --down, it removes the file's routes instead; the
file doesn't have to exist any more.

Create a commented example with 'sb init'. See docs/project-config.md.`,
		Example: "  sb apply\n  sb apply ../shop\n  sb apply --down",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := projectPath(args, down)
			if err != nil {
				return err
			}
			var routes []config.Route
			if !down {
				if routes, err = config.LoadProject(path); err != nil {
					return err
				}
			}
			c, err := newClient()
			if err != nil {
				return err
			}
			res, err := c.Apply(cmd.Context(), path, routes)
			if err != nil {
				return err
			}
			out, errOut := cmd.OutOrStdout(), cmd.ErrOrStderr()
			changed := len(res.Added) + len(res.Updated) + len(res.Removed)
			switch {
			case down && changed == 0:
				fmt.Fprintf(out, "No routes from %s to remove.\n", shortPath(path))
			case down:
				fmt.Fprintf(out, "Removed %d route(s) from %s:\n", len(res.Removed), shortPath(path))
			case changed == 0:
				fmt.Fprintf(out, "Up to date: %d route(s) from %s.\n", len(res.Unchanged), shortPath(path))
			default:
				fmt.Fprintf(out, "Applied %s:\n", shortPath(path))
			}
			for _, r := range res.Added {
				fmt.Fprintf(out, "  + %s\n", describeRoute(r))
			}
			for _, r := range res.Updated {
				fmt.Fprintf(out, "  ~ %s\n", describeRoute(r))
			}
			for _, r := range res.Removed {
				fmt.Fprintf(out, "  - %s\n", displayName(r))
			}
			for _, c := range res.Conflicts {
				fmt.Fprintf(errOut, "warning: skipped %s: already taken by %s; rename it in %s, or remove the other route\n",
					c.Name, c.Owner, shortPath(path))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&down, "down", false, "remove the file's routes instead of applying them")
	return cmd
}

// projectPath resolves the project file from args. Symlinks in its directory
// are resolved, so the same file always has the same source tag.
func projectPath(args []string, down bool) (string, error) {
	var path string
	if len(args) == 0 {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		if path, err = config.FindProject(wd); err != nil {
			return "", err
		}
	} else {
		abs, err := filepath.Abs(args[0])
		if err != nil {
			return "", err
		}
		path = abs
		if fi, err := os.Stat(abs); err == nil && fi.IsDir() {
			path = filepath.Join(abs, config.ProjectFileName)
		} else if err != nil && (!down || !errors.Is(err, fs.ErrNotExist)) {
			return "", fmt.Errorf("%s: %w", args[0], err)
		}
	}
	if dir, err := filepath.EvalSymlinks(filepath.Dir(path)); err == nil {
		path = filepath.Join(dir, filepath.Base(path))
	}
	return path, nil
}

func describeRoute(r config.Route) string {
	s := fmt.Sprintf("%s -> 127.0.0.1:%d", displayName(r), r.Port)
	if !r.RedirectHTTPS {
		s += " (no HTTPS redirect)"
	}
	return s
}

// shortPath shows path relative to the current directory when it is below
// it, else with the home directory as ~.
func shortPath(path string) string {
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, path); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(path, home+string(filepath.Separator)) {
		return "~" + path[len(home):]
	}
	return path
}

func newInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init [dir]",
		Short: "Write an example switchboard.toml for a project",
		Long: `Write a commented example switchboard.toml in dir (default: the current
directory), with a route named after the directory. Edit it, then run
'sb apply'. It never overwrites an existing file.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			abs, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			path := filepath.Join(abs, config.ProjectFileName)
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644) //nolint:gosec // G302: a project file, meant to be committed
			if errors.Is(err, fs.ErrExist) {
				return fmt.Errorf("%s already exists; edit it, then run 'sb apply'", shortPath(path))
			}
			if err != nil {
				return fmt.Errorf("init: %w", err)
			}
			if _, err := f.WriteString(config.ProjectTemplate(config.ProjectName(abs))); err != nil {
				_ = f.Close()
				return fmt.Errorf("init: %w", err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("init: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s. Edit it, then run 'sb apply'.\n", shortPath(path))
			return nil
		},
	}
}
