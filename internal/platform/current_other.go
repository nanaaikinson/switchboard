//go:build !darwin && !linux && !windows

package platform

import (
	"errors"
	"runtime"
)

type unsupported struct{}

func (unsupported) OpenURL(string) error {
	return errors.New("opening a browser is not supported on " + runtime.GOOS)
}

// Current returns the platform for this build.
func Current() Platform { return unsupported{} }
