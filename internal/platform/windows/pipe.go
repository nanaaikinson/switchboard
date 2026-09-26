package windows

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// PipeIDFile is the file in the config dir that holds the random part of the
// pipe name.
const PipeIDFile = "pipe-id"

// ErrForeignPipe means another account serves or holds the control pipe's
// name, so it isn't this user's daemon.
var ErrForeignPipe = errors.New("the control pipe belongs to another account")

// PipeName is the control API's named pipe for the user with sid, the config
// dir and id (from PipeID), like the Unix socket at <config dir>/sb.sock.
// All three are hashed in: another config dir (tests, SWITCHBOARD_CONFIG_DIR)
// gets its own pipe, the name reveals none of them, and since id is random
// and private another account can't work the name out and create the pipe
// first, which would keep the daemon from starting.
func PipeName(sid, configDir, id string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(sid) + "\x00" + strings.ToLower(configDir) + "\x00" + id))
	return `\\.\pipe\switchboard-` + hex.EncodeToString(sum[:8])
}

// PipeID returns the random id in dir's PipeIDFile, creating the file on
// first use. The daemon and the CLI both call it, so it is published
// atomically: whoever comes second reads the first one's id.
func PipeID(dir string) (string, error) {
	path := filepath.Join(dir, PipeIDFile)
	for {
		b, err := os.ReadFile(filepath.Clean(path))
		if err == nil {
			id := strings.TrimSpace(string(b))
			if len(id) != 32 || strings.Trim(id, "0123456789abcdef") != "" {
				return "", fmt.Errorf("%s is corrupt; delete it and start the daemon again", path)
			}
			return id, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
		switch err := createPipeID(dir, path); {
		case errors.Is(err, fs.ErrExist):
			continue // made meanwhile by the daemon or another sb
		case err != nil:
			return "", err
		}
	}
}

func createPipeID(dir, path string) error {
	raw := make([]byte, 16)
	_, _ = rand.Read(raw) // never fails
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+PipeIDFile+".tmp-*")
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.WriteString(hex.EncodeToString(raw) + "\n")
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// A hard link fails if path exists, unlike a rename on Windows.
	if err := os.Link(tmp.Name(), path); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fs.ErrExist
		}
		return fmt.Errorf("create %s: %w", path, err)
	}
	return nil
}

// PipeSDDL is the pipe's security descriptor: a protected DACL that allows
// only the user with sid, so no other account can connect.
func PipeSDDL(sid string) string { return "D:P(A;;GA;;;" + sid + ")" }
