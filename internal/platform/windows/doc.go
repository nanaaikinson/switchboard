// Package windows is Switchboard's Windows platform layer: the control pipe,
// NRPT split DNS, the daemon's logon task and the LocalMachine\Root store.
// Pure logic (script builders and parsers) builds on every OS so it can be
// tested anywhere; the rest is windows-only.
package windows
