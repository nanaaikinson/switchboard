package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// ErrBadSignature means a file doesn't match its minisign signature.
var ErrBadSignature = errors.New("signature verification failed")

// PublicKey is a minisign Ed25519 public key.
type PublicKey struct {
	id  [8]byte
	key ed25519.PublicKey
}

// ParsePublicKey reads a minisign public key: its base64 line ("RW..."), or
// the whole .pub file with its untrusted comment.
func ParsePublicKey(s string) (PublicKey, error) {
	line := strings.TrimSpace(s)
	if lines := nonEmptyLines(s); len(lines) == 2 && strings.HasPrefix(lines[0], "untrusted comment:") {
		line = lines[1]
	}
	raw, err := base64.StdEncoding.DecodeString(line)
	if err != nil || len(raw) != 2+8+ed25519.PublicKeySize || string(raw[:2]) != "Ed" {
		return PublicKey{}, errors.New("not a minisign Ed25519 public key")
	}
	var pk PublicKey
	copy(pk.id[:], raw[2:10])
	pk.key = ed25519.PublicKey(bytes.Clone(raw[10:]))
	return pk, nil
}

// KeyID is the key's ID as minisign prints it.
func (pk PublicKey) KeyID() string { return fmt.Sprintf("%016X", reverse(pk.id)) }

func reverse(b [8]byte) [8]byte {
	for i, j := 0, 7; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return b
}

// Verify checks a minisign signature file (the .minisig contents) for
// message and returns its trusted comment. It accepts both legacy ("Ed")
// signatures over the message and pre-hashed ("ED") signatures over its
// BLAKE2b-512 hash, which minisign 0.10+ and Tauri's signer make. The trusted
// comment is itself signed, so callers can rely on it.
func (pk PublicKey) Verify(message, sigFile []byte) (trustedComment string, err error) {
	lines := nonEmptyLines(string(sigFile))
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "untrusted comment:") || !strings.HasPrefix(lines[2], "trusted comment: ") {
		return "", fmt.Errorf("%w: malformed minisign signature", ErrBadSignature)
	}
	sig, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(sig) != 2+8+ed25519.SignatureSize {
		return "", fmt.Errorf("%w: malformed signature line", ErrBadSignature)
	}
	global, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil || len(global) != ed25519.SignatureSize {
		return "", fmt.Errorf("%w: malformed trusted comment signature", ErrBadSignature)
	}
	if !bytes.Equal(sig[2:10], pk.id[:]) {
		return "", fmt.Errorf("%w: signed with key %016X, not the update key %s", ErrBadSignature, reverse([8]byte(sig[2:10])), pk.KeyID())
	}
	signed := message
	switch string(sig[:2]) {
	case "Ed":
	case "ED":
		h := blake2b.Sum512(message)
		signed = h[:]
	default:
		return "", fmt.Errorf("%w: unknown signature algorithm %q", ErrBadSignature, sig[:2])
	}
	if !ed25519.Verify(pk.key, signed, sig[10:]) {
		return "", fmt.Errorf("%w: the file doesn't match its signature", ErrBadSignature)
	}
	comment := strings.TrimPrefix(lines[2], "trusted comment: ")
	if !ed25519.Verify(pk.key, append(bytes.Clone(sig[10:]), comment...), global) {
		return "", fmt.Errorf("%w: the trusted comment was changed", ErrBadSignature)
	}
	return comment, nil
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if l = strings.TrimRight(l, " \t"); l != "" {
			out = append(out, l)
		}
	}
	return out
}
