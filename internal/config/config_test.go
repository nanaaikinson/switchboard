package config

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
	}{
		{"empty", New()},
		{"single", &Config{SchemaVersion: SchemaVersion, Routes: []Route{
			{Name: "myapp.test", Port: 7000, RedirectHTTPS: true},
		}}},
		{"many", &Config{SchemaVersion: SchemaVersion, Routes: []Route{
			{Name: "myapp.test", Port: 7000, RedirectHTTPS: true},
			{Name: "api.myapp.test", Port: 7001},
			{Name: "tenants.myapp.test", Port: 3000, Wildcard: true, RedirectHTTPS: true},
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "nested", FileName)
			if err := Save(path, tt.cfg); err != nil {
				t.Fatalf("Save: %v", err)
			}
			got, err := Load(path)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(got, tt.cfg) {
				t.Errorf("round trip mismatch:\n got  %+v\n want %+v", got, tt.cfg)
			}
		})
	}
}

func TestSaveOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	first := &Config{SchemaVersion: SchemaVersion, Routes: []Route{{Name: "a.test", Port: 1}}}
	second := &Config{SchemaVersion: SchemaVersion, Routes: []Route{{Name: "b.test", Port: 2}}}
	for _, c := range []*Config{first, second} {
		if err := Save(path, c); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, second) {
		t.Errorf("got %+v, want %+v", got, second)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("expected only %s in dir, found %d entries (leftover temp files?)", FileName, len(entries))
	}
}

func TestSaveFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permissions only")
	}
	path := filepath.Join(t.TempDir(), "cfg", FileName)
	if err := Save(path, New()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	for p, want := range map[string]os.FileMode{path: 0o600, filepath.Dir(path): 0o700} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if got := fi.Mode().Perm(); got != want {
			t.Errorf("%s mode = %o, want %o", p, got, want)
		}
	}
}

func TestLoadMissingFile(t *testing.T) {
	got, err := Load(filepath.Join(t.TempDir(), "does-not-exist.toml"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, New()) {
		t.Errorf("got %+v, want empty config", got)
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    *Config
		wantErr string
	}{
		{
			name:    "no routes",
			content: "schema_version = 1\n",
			want:    New(),
		},
		{
			name: "defaults for omitted bools",
			content: `schema_version = 1
[[routes]]
name = "myapp.test"
port = 7000
`,
			want: &Config{SchemaVersion: 1, Routes: []Route{{Name: "myapp.test", Port: 7000}}},
		},
		{name: "missing schema_version", content: "", wantErr: "missing schema_version"},
		{name: "schema_version zero", content: "schema_version = 0\n", wantErr: "missing schema_version"},
		{name: "negative schema_version", content: "schema_version = -1\n", wantErr: "invalid schema_version"},
		{name: "newer schema_version", content: "schema_version = 2\n", wantErr: "upgrade sb"},
		{name: "malformed toml", content: "schema_version = \n", wantErr: "parse config"},
		{name: "unknown top-level key", content: "schema_version = 1\ntld = \"test\"\n", wantErr: "unknown keys tld"},
		{
			name: "unknown route key",
			content: `schema_version = 1
[[routes]]
name = "a.test"
port = 1
prot = 2
`,
			wantErr: "unknown keys routes.prot",
		},
		{
			name: "invalid route",
			content: `schema_version = 1
[[routes]]
name = "a.test"
port = 0
`,
			wantErr: "out of range",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := Load(path)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		routes  []Route
		wantErr string
	}{
		{name: "ok", routes: []Route{{Name: "a.test", Port: 1}, {Name: "b.test", Port: 65535}}},
		{name: "empty name", routes: []Route{{Name: " ", Port: 80}}, wantErr: "name is empty"},
		{name: "port zero", routes: []Route{{Name: "a.test", Port: 0}}, wantErr: "out of range"},
		{name: "port too high", routes: []Route{{Name: "a.test", Port: 65536}}, wantErr: "out of range"},
		{name: "duplicate", routes: []Route{{Name: "a.test", Port: 1}, {Name: "a.test", Port: 2}}, wantErr: "duplicate"},
		{name: "duplicate case-insensitive", routes: []Route{{Name: "a.test", Port: 1}, {Name: "A.Test", Port: 2}}, wantErr: "duplicate"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&Config{SchemaVersion: SchemaVersion, Routes: tt.routes}).Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestSaveRejectsInvalid(t *testing.T) {
	tests := []struct {
		name string
		cfg  *Config
	}{
		{"wrong schema", &Config{SchemaVersion: 2}},
		{"bad route", &Config{SchemaVersion: SchemaVersion, Routes: []Route{{Name: "a.test"}}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := Save(path, tt.cfg); err == nil {
				t.Fatal("expected error")
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("file written despite error (stat err: %v)", err)
			}
		})
	}
}

func TestDir(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix path rules")
	}
	home := t.TempDir()
	tests := []struct {
		name     string
		override string
		xdg      string
		want     string
	}{
		{name: "override wins", override: "/custom/sb", xdg: "/xdg", want: "/custom/sb"},
		{name: "xdg", xdg: "/xdg", want: "/xdg/switchboard"},
		{name: "relative xdg ignored", xdg: "rel/xdg", want: filepath.Join(home, ".config", "switchboard")},
		{name: "home fallback", want: filepath.Join(home, ".config", "switchboard")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("HOME", home)
			t.Setenv(EnvConfigDir, tt.override)
			t.Setenv("XDG_CONFIG_HOME", tt.xdg)
			got, err := Dir()
			if err != nil {
				t.Fatalf("Dir: %v", err)
			}
			if got != tt.want {
				t.Errorf("Dir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDefaultPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(EnvConfigDir, dir)
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	if want := filepath.Join(dir, FileName); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}
