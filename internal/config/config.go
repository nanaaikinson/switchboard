// Package config loads and saves the Switchboard route table as TOML.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/BurntSushi/toml"
)

// SchemaVersion is the config schema version this build reads and writes.
const SchemaVersion = 1

// FileName is the name of the route table file inside the config dir.
const FileName = "routes.toml"

// EnvConfigDir overrides the config directory when set.
const EnvConfigDir = "SWITCHBOARD_CONFIG_DIR"

// Route maps a hostname to a local port.
type Route struct {
	Name          string `toml:"name" json:"name"`
	Port          int    `toml:"port" json:"port"`
	Wildcard      bool   `toml:"wildcard" json:"wildcard"`
	RedirectHTTPS bool   `toml:"redirect_https" json:"redirect_https"`
	// File is the absolute path of the switchboard.toml that 'sb apply'
	// added the route from; empty for routes added with 'sb add'.
	File string `toml:"file,omitempty" json:"file,omitempty"`
}

// TLD is a top-level domain served in addition to the default one.
type TLD struct {
	Name string `toml:"name" json:"name"` // e.g. "local", no dots
	// MDNS resolves names under the TLD by announcing each route over
	// multicast DNS instead of through split DNS. Experimental; only "local".
	MDNS bool `toml:"mdns,omitempty" json:"mdns,omitempty"`
}

// MDNSTLD is the only TLD that can be served over mDNS.
const MDNSTLD = "local"

// Config is the on-disk route table.
type Config struct {
	SchemaVersion int `toml:"schema_version"`
	// TLDs are served in addition to the default TLD. Optional; configs
	// written before it existed have none.
	TLDs   []TLD   `toml:"tlds,omitempty"`
	Routes []Route `toml:"routes"`
}

// New returns an empty config at the current schema version.
func New() *Config {
	return &Config{SchemaVersion: SchemaVersion, Routes: []Route{}}
}

// Dir returns the Switchboard config directory:
// $SWITCHBOARD_CONFIG_DIR if set, else %APPDATA%\switchboard on Windows,
// else $XDG_CONFIG_HOME/switchboard or ~/.config/switchboard.
func Dir() (string, error) {
	if d := os.Getenv(EnvConfigDir); d != "" {
		return d, nil
	}
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", errors.New("locate config dir: %APPDATA% is not set; set " + EnvConfigDir + " to choose a directory")
		}
		return filepath.Join(appData, "switchboard"), nil
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" && filepath.IsAbs(xdg) {
		return filepath.Join(xdg, "switchboard"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate config dir: %w; set %s to choose a directory", err, EnvConfigDir)
	}
	return filepath.Join(home, ".config", "switchboard"), nil
}

// DefaultPath returns the path of the route table file.
func DefaultPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, FileName), nil
}

// Load reads the config at path. A missing file yields an empty config.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if errors.Is(err, fs.ErrNotExist) {
		return New(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}

	cfg := &Config{}
	md, err := toml.Decode(string(data), cfg)
	if err != nil {
		return nil, fmt.Errorf("parse config %s: %w; fix the file or move it aside", path, err)
	}
	if undec := md.Undecoded(); len(undec) > 0 {
		keys := make([]string, len(undec))
		for i, k := range undec {
			keys[i] = k.String()
		}
		return nil, fmt.Errorf("parse config %s: unknown keys %s; remove them or upgrade sb", path, strings.Join(keys, ", "))
	}

	switch {
	case cfg.SchemaVersion == 0:
		return nil, fmt.Errorf("config %s: missing schema_version; add schema_version = %d", path, SchemaVersion)
	case cfg.SchemaVersion > SchemaVersion:
		return nil, fmt.Errorf("config %s: schema_version %d is newer than supported (%d); upgrade sb", path, cfg.SchemaVersion, SchemaVersion)
	case cfg.SchemaVersion < 0:
		return nil, fmt.Errorf("config %s: invalid schema_version %d", path, cfg.SchemaVersion)
	}

	if cfg.Routes == nil {
		cfg.Routes = []Route{}
	}
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Save validates cfg and writes it atomically to path with mode 0600.
func Save(path string, cfg *Config) error {
	if cfg.SchemaVersion != SchemaVersion {
		return fmt.Errorf("save config: schema_version %d, want %d", cfg.SchemaVersion, SchemaVersion)
	}
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after successful rename

	if _, err := tmp.Write(buf.Bytes()); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write temp config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync temp config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("chmod temp config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config %s: %w", path, err)
	}
	return nil
}

// Validate checks that every route has a name, a valid port and a unique
// name, and that extra TLDs are ones this build can serve.
func (c *Config) Validate() error {
	for _, t := range c.TLDs {
		switch {
		case t.Name != MDNSTLD:
			return fmt.Errorf("tld %q: only %q (with mdns = true) can be added; remove it", t.Name, MDNSTLD)
		case !t.MDNS:
			return fmt.Errorf("tld %q: needs mdns = true; .%s only works over multicast DNS", t.Name, MDNSTLD)
		}
	}
	if len(c.TLDs) > 1 {
		return fmt.Errorf("tld %q: listed more than once; remove the duplicate", c.TLDs[1].Name)
	}
	seen := make(map[string]bool, len(c.Routes))
	for i, r := range c.Routes {
		if strings.TrimSpace(r.Name) == "" {
			return fmt.Errorf("route %d: name is empty", i)
		}
		if r.Port < 1 || r.Port > 65535 {
			return fmt.Errorf("route %q: port %d out of range 1-65535", r.Name, r.Port)
		}
		key := strings.ToLower(r.Name)
		if seen[key] {
			return fmt.Errorf("route %q: duplicate name; remove one with `sb rm %s`", r.Name, r.Name)
		}
		seen[key] = true
	}
	return nil
}
