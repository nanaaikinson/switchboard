package api

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nanaaikinson/switchboard/internal/config"
	pwin "github.com/nanaaikinson/switchboard/internal/platform/windows"
)

func TestDefaultPipeIsStableAndRandom(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(config.EnvConfigDir, dir)
	a, err := DefaultSocketPath()
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := DefaultSocketPath(); a != b {
		t.Errorf("pipe changed between calls: %s, %s", a, b)
	}
	t.Setenv(config.EnvConfigDir, t.TempDir()) // another install
	if c, _ := DefaultSocketPath(); c == a {
		t.Error("two installs share a pipe name")
	}
	t.Setenv(config.EnvConfigDir, dir)
	tests := []struct {
		err  error
		want string // "" for unchanged
	}{
		{fmt.Errorf("%w: served by X", pwin.ErrForeignPipe), "Delete " + filepath.Join(dir, pwin.PipeIDFile)},
		{errors.New("other"), ""},
	}
	for _, tt := range tests {
		got := ExplainForeignPipe(tt.err)
		if tt.want == "" && got.Error() != tt.err.Error() || tt.want != "" && (!errors.Is(got, pwin.ErrForeignPipe) || !strings.Contains(got.Error(), tt.want)) {
			t.Errorf("ExplainForeignPipe(%v) = %v, want %q", tt.err, got, tt.want)
		}
	}
}
