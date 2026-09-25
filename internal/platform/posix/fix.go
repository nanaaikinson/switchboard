//go:build darwin || linux

package posix

import "fmt"

// FixError is a diagnostic failure that knows its one-line fix. sb doctor
// finds the fix with errors.As on interface{ Fix() string }.
type FixError struct {
	Err error
	fix string
}

func (e *FixError) Error() string { return e.Err.Error() }
func (e *FixError) Unwrap() error { return e.Err }

// Fix is the one-line fix.
func (e *FixError) Fix() string { return e.fix }

// WithFix returns a FixError with a formatted message.
func WithFix(fix, format string, args ...any) error {
	return &FixError{Err: fmt.Errorf(format, args...), fix: fix}
}

// ValidTLD reports whether tld is a single lowercase LDH label.
func ValidTLD(tld string) bool {
	if tld == "" || len(tld) > 63 {
		return false
	}
	for _, c := range tld {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	return tld[0] != '-' && tld[len(tld)-1] != '-'
}
