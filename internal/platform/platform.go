// Package platform exposes OS-specific behaviour behind an interface. The
// implementation for the build target lives in internal/platform/<os>.
package platform

import (
	"fmt"
	"net/url"
)

// Platform is the OS-specific functionality Switchboard needs.
type Platform interface {
	// OpenURL opens an http(s) URL in the user's default browser.
	OpenURL(rawURL string) error
}

// OpenURL validates rawURL and opens it with the current platform.
func OpenURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("open %q: only http(s) URLs can be opened", rawURL)
	}
	if err := Current().OpenURL(u.String()); err != nil {
		return fmt.Errorf("open %s in browser: %w; open it manually", u, err)
	}
	return nil
}
