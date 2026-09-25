package windows

import "os/exec"

// Platform implements platform.Platform for windows.
type Platform struct{}

// OpenURL opens rawURL, already validated as http(s), in the default browser.
func (Platform) OpenURL(rawURL string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL).Start() //nolint:gosec // G204: fixed binary; URL validated by platform.OpenURL
}
