package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaaikinson/switchboard/internal/api"
	"github.com/nanaaikinson/switchboard/internal/config"
)

func wantAll(t *testing.T, out string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(out, p) {
			t.Errorf("output missing %q:\n%s", p, out)
		}
	}
}

func TestInitAndApply(t *testing.T) {
	dir := configDir(t)
	startDaemon(t, daemonOptions{})
	repo, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	repo = filepath.Join(repo, "Shop")
	sub := filepath.Join(repo, "web", "src")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repo, config.ProjectFileName)
	t.Chdir(sub)

	if _, err := run(t, "apply"); err == nil || !strings.Contains(err.Error(), "sb init") {
		t.Errorf("apply without a file: %v", err)
	}
	wantAll(t, mustRun(t, "init", repo), "Wrote ", "switchboard.toml. Edit it, then run 'sb apply'.")
	if _, err := run(t, "init", repo); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Errorf("second init: %v", err)
	}

	// Found from a subdirectory, up to the git root.
	wantAll(t, mustRun(t, "apply"), "Applied ", "+ shop.test -> 127.0.0.1:3000")
	wantAll(t, mustRun(t, "apply"), "Up to date: 1 route(s)")
	wantAll(t, mustRun(t, "ls"), "shop.test", "file (")

	var rs []api.RouteStatus
	if err := json.Unmarshal([]byte(mustRun(t, "ls", "--json")), &rs); err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Source != api.SourceFile || rs[0].File != file {
		t.Errorf("routes = %+v, want tagged with %s", rs, file)
	}

	// A route someone added by hand is never overwritten.
	mustRun(t, "add", "admin.shop", "9000")
	body := "[routes]\nshop = 3100\n\"api.shop\" = { port = 4000, redirect = false }\n\"admin.shop\" = 4001\n"
	if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	out := mustRun(t, "apply", repo)
	wantAll(t, out, "~ shop.test -> 127.0.0.1:3100", "+ api.shop.test -> 127.0.0.1:4000 (no HTTPS redirect)",
		"warning: skipped admin.shop.test: already taken by a route added with 'sb add'")
	cfg, err := config.Load(filepath.Join(dir, config.FileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range cfg.Routes {
		if r.Name == "admin.shop.test" && (r.Port != 9000 || r.File != "") {
			t.Errorf("manual route overwritten: %+v", r)
		}
	}
	// Idempotent, conflicts included: the warning repeats, nothing changes.
	out = mustRun(t, "apply", file)
	wantAll(t, out, "Up to date: 2 route(s)", "warning: skipped admin.shop.test")

	// Dropping a line removes only that route.
	if err := os.WriteFile(file, []byte("[routes]\nshop = 3100\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wantAll(t, mustRun(t, "apply"), "- api.shop.test")

	// --down works even after the file is deleted, and keeps other routes.
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	wantAll(t, mustRun(t, "apply", "--down", file), "Removed 1 route(s)", "- shop.test")
	wantAll(t, mustRun(t, "apply", "--down", file), "No routes from")
	if err := json.Unmarshal([]byte(mustRun(t, "ls", "--json")), &rs); err != nil {
		t.Fatal(err)
	}
	if len(rs) != 1 || rs[0].Name != "admin.shop.test" {
		t.Errorf("after down: %+v", rs)
	}

	if _, err := run(t, "apply", filepath.Join(repo, "missing.toml")); err == nil {
		t.Error("apply of a missing file succeeded")
	}
}
