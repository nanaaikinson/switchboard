package darwin

import "os/exec"

// OpenURL opens rawURL, already validated as http(s), in the default browser.
func (p *Platform) OpenURL(rawURL string) error {
	return exec.Command("open", rawURL).Start() //nolint:gosec // G204: fixed binary; URL validated by platform.OpenURL
}
