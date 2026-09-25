package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

// ProjectFileName is the per-project routes file read by 'sb apply'.
const ProjectFileName = "switchboard.toml"

// ErrNoProjectFile means FindProject found no switchboard.toml.
var ErrNoProjectFile = errors.New("no " + ProjectFileName + " found")

// LoadProject reads a switchboard.toml. Its [routes] table maps names to a
// port, or to a table { port = 3000, redirect = false }. Routes redirect HTTP
// to HTTPS unless redirect = false. Names are returned as written, sorted;
// the daemon adds the TLD.
func LoadProject(path string) ([]Route, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var raw struct {
		Routes map[string]toml.Primitive `toml:"routes"`
	}
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	routes := make([]Route, 0, len(raw.Routes))
	for name, prim := range raw.Routes {
		r := Route{Name: strings.TrimSpace(name), RedirectHTTPS: true}
		var entry struct {
			Port     int   `toml:"port"`
			Redirect *bool `toml:"redirect"`
		}
		switch {
		case md.Type("routes", name) == "Integer":
			if err := md.PrimitiveDecode(prim, &r.Port); err != nil {
				return nil, fmt.Errorf("%s: routes.%q: %w", path, name, err)
			}
		case md.Type("routes", name) == "Hash":
			if err := md.PrimitiveDecode(prim, &entry); err != nil {
				return nil, fmt.Errorf("%s: routes.%q: %w", path, name, err)
			}
			if entry.Port == 0 {
				return nil, fmt.Errorf("%s: routes.%q: missing port; write { port = 3000 }", path, name)
			}
			r.Port = entry.Port
			if entry.Redirect != nil {
				r.RedirectHTTPS = *entry.Redirect
			}
		default:
			return nil, fmt.Errorf("%s: routes.%q: want a port like 3000 or a table like { port = 3000, redirect = false }", path, name)
		}
		if r.Name == "" {
			return nil, fmt.Errorf("%s: a route has an empty name", path)
		}
		if r.Port < 1 || r.Port > 65535 {
			return nil, fmt.Errorf("%s: routes.%q: port %d out of range 1-65535", path, name, r.Port)
		}
		routes = append(routes, r)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		keys := make([]string, len(undec))
		for i, k := range undec {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("%s: unknown keys %s; routes take only port and redirect", path, strings.Join(keys, ", "))
	}
	slices.SortFunc(routes, func(a, b Route) int { return strings.Compare(a.Name, b.Name) })
	return routes, nil
}

// FindProject looks for switchboard.toml in dir and each parent up to the
// git repository root (the first directory with a .git entry). Outside a git
// repository only dir itself is searched. It returns an absolute path.
func FindProject(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	root := gitRoot(dir)
	if root == "" {
		root = dir
	}
	for cur := dir; ; cur = filepath.Dir(cur) {
		path := filepath.Join(cur, ProjectFileName)
		if fi, err := os.Stat(path); err == nil && fi.Mode().IsRegular() {
			return path, nil
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		if cur == root || filepath.Dir(cur) == cur {
			break
		}
	}
	if root == dir {
		return "", fmt.Errorf("%w in %s; create one with 'sb init'", ErrNoProjectFile, dir)
	}
	return "", fmt.Errorf("%w in %s or its parents up to the git root %s; create one with 'sb init'", ErrNoProjectFile, dir, root)
}

// gitRoot is the nearest directory at or above dir with a .git entry (a
// directory, or a file in worktrees and submodules), or "".
func gitRoot(dir string) string {
	for cur := dir; ; cur = filepath.Dir(cur) {
		if _, err := os.Lstat(filepath.Join(cur, ".git")); err == nil {
			return cur
		}
		if filepath.Dir(cur) == cur {
			return ""
		}
	}
}

// ProjectTemplate is the commented example 'sb init' writes. name is used for
// the example routes and must be a hostname label.
func ProjectTemplate(name string) string {
	return `# Switchboard routes for this project.
#
#   sb apply          add or update these routes (run it again after editing)
#   sb apply --down   remove them
#
# Names without a TLD get the default one: "` + name + `" is https://` + name + `.test.
# A name starting with "*." also matches every subdomain. Names that another
# route already has (from 'sb add', another project or a Docker container)
# are skipped with a warning. See docs/project-config.md.

[routes]
# "name" = port on 127.0.0.1
"` + name + `" = 3000

# A table sets options. redirect = false serves plain HTTP too, instead of
# redirecting it to HTTPS.
# "api.` + name + `" = { port = 4000, redirect = false }

# Every subdomain, e.g. https://acme.tenants.` + name + `.test
# "*.tenants.` + name + `" = 3000
`
}

// ProjectName turns a directory name into a hostname label for
// ProjectTemplate: lowercase, with other characters replaced by hyphens.
func ProjectName(dir string) string {
	b := []byte(strings.ToLower(filepath.Base(dir)))
	for i, c := range b {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			b[i] = '-'
		}
	}
	if name := strings.Trim(string(b), "-"); name != "" && len(name) <= 63 {
		return name
	}
	return "myapp"
}
