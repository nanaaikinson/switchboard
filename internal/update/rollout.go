package update

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// InstallIDFile holds a random ID for this install, in the config dir. It is
// only used to place the install in a rollout bucket and is never sent
// anywhere.
const InstallIDFile = "install-id"

// InstallID reads the install ID from dir, creating it on first use.
func InstallID(dir string) (string, error) {
	path := filepath.Join(dir, InstallIDFile)
	b, err := os.ReadFile(path) //nolint:gosec // G304: our own config dir
	if err == nil {
		if id := strings.TrimSpace(string(b)); len(id) == 32 {
			if _, err := hex.DecodeString(id); err == nil {
				return id, nil
			}
		}
		// Unreadable content: replace it with a fresh ID below.
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("read install ID: %w", err)
	}
	raw := make([]byte, 16)
	_, _ = rand.Read(raw) // never fails
	id := hex.EncodeToString(raw)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(path, []byte(id+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("write install ID: %w", err)
	}
	return id, nil
}

// Bucket places an install in 0-99 for a release. It is stable for the same
// install and version, so repeated checks agree, and differs between
// versions, so the same installs aren't always first.
func Bucket(installID, version string) int {
	sum := sha256.Sum256([]byte(installID + "\x00" + strings.TrimPrefix(version, "v")))
	return int(binary.BigEndian.Uint32(sum[:4]) % 100)
}

// InRollout reports whether an install in bucket gets a release rolled out
// to percent of installs.
func InRollout(bucket, percent int) bool { return bucket < percent }
