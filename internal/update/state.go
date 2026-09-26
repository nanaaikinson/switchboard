package update

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// StateFile is kept in the config dir, next to InstallIDFile, and remembers
// the pub_date of the newest manifest this install accepted on each channel.
// A manifest older than that is refused: it can only be an old one served
// again, to hold the install back or steer it to an older release than it
// has been offered.
const StateFile = "update-state.json"

// MaxClockSkew is how far in the future a manifest's pub_date may be. A
// later one would be remembered and then refuse every real manifest until
// that time, so it is refused instead.
const MaxClockSkew = time.Hour

type updateState struct {
	PubDates map[string]string `json:"pub_dates"` // channel → RFC 3339
}

func readState(dir string) (updateState, error) {
	s := updateState{PubDates: map[string]string{}}
	b, err := os.ReadFile(filepath.Join(dir, StateFile)) //nolint:gosec // G304: our own config dir
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read update state: %w", err)
	}
	// Unreadable content starts over, as a fresh install would.
	if json.Unmarshal(b, &s) != nil || s.PubDates == nil {
		s.PubDates = map[string]string{}
	}
	return s, nil
}

// lastPubDate is the newest pub_date accepted on channel, or zero.
func lastPubDate(dir, channel string) (time.Time, error) {
	s, err := readState(dir)
	if err != nil {
		return time.Time{}, err
	}
	t, _ := time.Parse(time.RFC3339, s.PubDates[channel])
	return t, nil
}

// recordPubDate remembers pub as channel's newest accepted pub_date.
func recordPubDate(dir, channel string, pub time.Time) error {
	s, err := readState(dir)
	if err != nil {
		return err
	}
	s.PubDates[channel] = pub.UTC().Format(time.RFC3339)
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, StateFile), append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("write update state: %w", err)
	}
	return nil
}
