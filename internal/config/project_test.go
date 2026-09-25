package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeProject(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, ProjectFileName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func routeList(rs []Route) string {
	var out []string
	for _, r := range rs {
		out = append(out, fmt.Sprintf("%s:%d:%v", r.Name, r.Port, r.RedirectHTTPS))
	}
	return strings.Join(out, " ")
}

func TestLoadProject(t *testing.T) {
	tests := []struct {
		name, body, want, errText string
	}{
		{name: "ports and tables", body: `
[routes]
"web" = 3000
"api.web" = { port = 4000, redirect = false }
"*.tenants.web" = { port = 3000 }
admin = 3001
`, want: "*.tenants.web:3000:true admin:3001:true api.web:4000:false web:3000:true"},
		{name: "table form", body: "[routes.web]\nport = 3000\nredirect = true\n", want: "web:3000:true"},
		{name: "no routes", body: "", want: ""},
		{name: "empty routes", body: "[routes]\n", want: ""},
		{name: "string port", body: "[routes]\nweb = \"3000\"\n", errText: `routes."web": want a port like 3000`},
		{name: "port out of range", body: "[routes]\nweb = 70000\n", errText: "port 70000 out of range"},
		{name: "table without port", body: "[routes]\nweb = { redirect = false }\n", errText: "missing port"},
		{name: "unknown route key", body: "[routes]\nweb = { port = 3000, wildcard = true }\n", errText: "unknown keys routes.web.wildcard"},
		{name: "unknown top-level key", body: "tld = \"dev\"\n[routes]\nweb = 3000\n", errText: "unknown keys tld"},
		{name: "bad redirect type", body: "[routes]\nweb = { port = 3000, redirect = \"no\" }\n", errText: `routes."web"`},
		{name: "invalid TOML", body: "[routes\n", errText: "parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeProject(t, t.TempDir(), tt.body)
			rs, err := LoadProject(path)
			if tt.errText != "" {
				if err == nil || !strings.Contains(err.Error(), tt.errText) || !strings.Contains(err.Error(), path) {
					t.Fatalf("err = %v, want one naming %s and containing %q", err, path, tt.errText)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := routeList(rs); got != tt.want {
				t.Errorf("routes = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindProject(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	deep := filepath.Join(repo, "services", "api", "src")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Above the git root: must never be picked up from inside the repo.
	writeProject(t, root, "[routes]\nouter = 1\n")

	if _, err := FindProject(deep); !errors.Is(err, ErrNoProjectFile) || !strings.Contains(err.Error(), "up to the git root "+repo) {
		t.Errorf("none in repo: %v", err)
	}
	atRoot := writeProject(t, repo, "[routes]\nweb = 3000\n")
	if got, err := FindProject(deep); err != nil || got != atRoot {
		t.Errorf("FindProject = %s, %v; want %s", got, err, atRoot)
	}
	nearer := writeProject(t, filepath.Join(repo, "services", "api"), "[routes]\napi = 4000\n")
	if got, _ := FindProject(deep); got != nearer {
		t.Errorf("FindProject = %s; want the nearest, %s", got, nearer)
	}

	// A worktree's .git is a file.
	wt := filepath.Join(root, "wt")
	if err := os.MkdirAll(filepath.Join(wt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := FindProject(filepath.Join(wt, "sub")); !errors.Is(err, ErrNoProjectFile) {
		t.Errorf("worktree: %v", err)
	}

	// Outside any repo only the directory itself counts.
	plain := filepath.Join(t.TempDir(), "plain", "sub")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	writeProject(t, filepath.Dir(plain), "[routes]\nx = 1\n")
	if _, err := FindProject(plain); !errors.Is(err, ErrNoProjectFile) {
		t.Errorf("outside a repo, a parent's file was used: %v", err)
	}
}

func TestProjectTemplateParses(t *testing.T) {
	name := ProjectName("/home/me/My Shop_2")
	if name != "my-shop-2" {
		t.Errorf("ProjectName = %q", name)
	}
	if ProjectName("/") != "myapp" || ProjectName("/x/___") != "myapp" {
		t.Error("ProjectName fallback")
	}
	path := writeProject(t, t.TempDir(), ProjectTemplate(name))
	rs, err := LoadProject(path)
	if err != nil {
		t.Fatalf("the sb init template does not parse: %v", err)
	}
	if routeList(rs) != "my-shop-2:3000:true" {
		t.Errorf("template routes = %s", routeList(rs))
	}
	// Uncommenting the examples gives valid routes too.
	uncommented := strings.NewReplacer(`# "api.`, `"api.`, `# "*.`, `"*.`).Replace(ProjectTemplate(name))
	rs, err = LoadProject(writeProject(t, t.TempDir(), uncommented))
	if err != nil || len(rs) != 3 {
		t.Errorf("uncommented template: %v, %s", err, routeList(rs))
	}
}
