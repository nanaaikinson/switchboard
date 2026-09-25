package windows

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// PipeName is the control API's named pipe for the user with sid and the
// config dir, like the Unix socket at <config dir>/sb.sock. Both are hashed
// in, so another config dir (tests, SWITCHBOARD_CONFIG_DIR) gets its own
// pipe, and the name reveals neither.
func PipeName(sid, configDir string) string {
	sum := sha256.Sum256([]byte(strings.ToUpper(sid) + "\x00" + strings.ToLower(configDir)))
	return `\\.\pipe\switchboard-` + hex.EncodeToString(sum[:8])
}

// PipeSDDL is the pipe's security descriptor: a protected DACL that allows
// only the user with sid, so no other account can connect.
func PipeSDDL(sid string) string { return "D:P(A;;GA;;;" + sid + ")" }
