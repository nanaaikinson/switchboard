#!/bin/sh
# Runs before the .deb or .rpm is removed. It can't undo 'sb setup' for you,
# because that belongs to a user; it only warns if it was run.
if [ -e /etc/systemd/system/switchboard-helper.service ]; then
	echo "Switchboard: 'sb setup' was run on this machine. Its system changes stay after the package is removed."
	echo "To remove them, reinstall the package and run 'sb uninstall' as the user who ran 'sb setup'."
fi
