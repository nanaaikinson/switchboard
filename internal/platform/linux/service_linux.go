package linux

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/nanaaikinson/switchboard/internal/platform/posix"
)

// ErrNoHelper means the privileged helper is not installed or not running.
var ErrNoHelper = posix.ErrNoHelper

// helperUnit runs the helper as root at boot. It is sandboxed to what the
// helper does: bind 80/443, chown its socket, and rewrite /etc/hosts.
func helperUnit(uid int) []byte {
	return []byte(marker + `[Unit]
Description=Switchboard privileged helper (binds ports 80 and 443 for the daemon)
After=network.target

[Service]
ExecStart=` + helperBinPath + ` helper serve --uid ` + strconv.Itoa(uid) + `
Restart=always
RestartSec=1
RuntimeDirectory=switchboard
RuntimeDirectoryMode=0755
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=-/etc/hosts
ProtectHome=yes
PrivateTmp=yes
CapabilityBoundingSet=CAP_NET_BIND_SERVICE CAP_CHOWN
RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6

[Install]
WantedBy=multi-user.target
`)
}

// daemonUnit runs `sb daemon` in the user's systemd session. sbPath has been
// checked by Validate to need no quoting.
func daemonUnit(sbPath string) []byte {
	return []byte(marker + `[Unit]
Description=Switchboard daemon (DNS, proxy, control API)

[Service]
ExecStart=` + sbPath + ` daemon
Restart=on-failure
RestartSec=2

[Install]
WantedBy=default.target
`)
}

// InstallService installs and starts the helper's system unit, then the
// daemon's user unit. Re-running replaces and restarts both.
func (p *Platform) InstallService() error {
	gid, err := posix.UserGID(p.o.UID)
	if err != nil {
		return err
	}
	user, err := posix.UserName(p.o.UID)
	if err != nil {
		return err
	}
	if err := p.files.CopyRootOwned(p.o.SbPath, helperBinPath); err != nil {
		return err
	}
	if err := os.MkdirAll(p.fs(filepath.Dir(helperUnitPath)), 0o755); err != nil { //nolint:gosec // G301: systemd's unit dir
		return fmt.Errorf("install helper unit: %w", err)
	}
	if err := p.files.WriteFile(helperUnitPath, helperUnit(p.o.UID), 0o644, 0, 0); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", helperUnitName}, {"restart", helperUnitName}} {
		if err := p.run("systemctl "+args[0], "systemctl", args...); err != nil {
			return err
		}
	}

	home, err := p.files.OpenHome(p.o.Home)
	if err != nil {
		return err
	}
	defer home.Close()
	if err := home.MkdirAll(filepath.Dir(p.daemonUnit()), gid); err != nil {
		return err
	}
	if err := home.WriteFile(p.daemonUnit(), daemonUnit(p.o.SbPath), 0o644, gid); err != nil {
		return err
	}
	for _, args := range [][]string{{"daemon-reload"}, {"enable", daemonUnitName}, {"restart", daemonUnitName}} {
		if err := p.userSystemctl(user, args...); err != nil {
			return err
		}
	}
	return nil
}

// userSystemctl runs systemctl in the user's service manager.
func (p *Platform) userSystemctl(user string, args ...string) error {
	full := append([]string{"--user", "--machine=" + user + "@"}, args...)
	if out, err := p.o.Run("systemctl", full...); err != nil {
		return fmt.Errorf("systemctl --user %s: %w: %s; your systemd user session must be running: log in to a desktop session, or run 'loginctl enable-linger %s', then run 'sb setup' again",
			strings.Join(args, " "), err, strings.TrimSpace(string(out)), user)
	}
	return nil
}

// RemoveService stops and deletes everything InstallService created. It is
// safe to run when nothing is installed.
func (p *Platform) RemoveService() error {
	var errs []error
	if _, err := os.Lstat(p.fs(p.daemonUnit())); err == nil {
		user, err := posix.UserName(p.o.UID)
		if err == nil {
			errs = append(errs, p.userSystemctl(user, "disable", "--now", daemonUnitName))
		} else {
			errs = append(errs, err)
		}
		errs = append(errs, p.files.RemoveUserFile(p.o.Home, p.daemonUnit()))
		if err == nil {
			_ = p.userSystemctl(user, "daemon-reload") // best effort; the unit is gone either way
		}
	}
	if _, err := os.Lstat(p.fs(helperUnitPath)); err == nil {
		errs = append(errs, p.run("stop helper", "systemctl", "disable", "--now", helperUnitName))
		errs = append(errs, p.files.Remove(helperUnitPath))
		errs = append(errs, p.run("systemctl daemon-reload", "systemctl", "daemon-reload"))
	}
	errs = append(errs, p.files.Remove(helperBinPath))
	if err := os.Remove(p.fs(filepath.Dir(helperBinPath))); err != nil && !errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, fmt.Errorf("remove %s: %w", filepath.Dir(helperBinPath), err))
	}
	return errors.Join(errs...)
}

// ServeHelper runs the root side of the helper protocol until ctx is done,
// including the "hosts" op for the /etc/hosts fallback.
func (p *Platform) ServeHelper(ctx context.Context) error {
	gid, err := posix.UserGID(p.o.UID)
	if err != nil {
		return err
	}
	s := &posix.Server{Socket: p.o.HelperSocket, UID: p.o.UID, GID: gid, Addrs: p.o.HelperAddrs, Hosts: p.syncHosts}
	return s.Serve(ctx)
}

// HelperListeners asks the helper for the HTTP and HTTPS listening sockets.
func (p *Platform) HelperListeners(ctx context.Context) ([]net.Listener, error) {
	return posix.Listeners(ctx, p.o.HelperSocket)
}

// SyncHosts asks the helper to list names in the /etc/hosts block. It does
// nothing when split DNS is handled by systemd-resolved or dnsmasq.
func (p *Platform) SyncHosts(ctx context.Context, names []string) error {
	return posix.SyncHosts(ctx, p.o.HelperSocket, names)
}

// RestartDaemon restarts the daemon's systemd user unit, so it runs the sb
// binary now at its path. It reports false, and does nothing, if the daemon
// isn't installed as a service. It runs as the user, in their own session.
func (p *Platform) RestartDaemon() (bool, error) {
	if _, err := os.Stat(p.fs(p.daemonUnit())); err != nil {
		return false, nil
	}
	return true, p.run("restart the daemon", "systemctl", "--user", "restart", daemonUnitName)
}
