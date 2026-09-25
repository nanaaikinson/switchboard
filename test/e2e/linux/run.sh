#!/bin/sh
# The Linux end-to-end test, run as root inside the e2e container (see
# e2e.sh). Everything Switchboard does is done as the normal user "dev", the
# way a person would: sb setup, sb add, curl, sb doctor, sb uninstall. It runs
# once with systemd-resolved, then again with the /etc/hosts fallback.
set -eu

fail() {
	echo "e2e: FAIL: $*" >&2
	exit 1
}
step() { echo "--- $*"; }

# retry CMD... runs CMD until it succeeds, for up to 30 seconds.
retry() {
	i=0
	until "$@" >/dev/null 2>&1; do
		i=$((i + 1))
		[ "$i" -lt 30 ] || fail "timed out waiting for: $*"
		sleep 1
	done
}

systemctl is-system-running --wait >/dev/null 2>&1 || true
uid=$(id -u dev)
ca=/home/dev/.config/switchboard/pki/ca/ca.pem
retry systemctl is-active --quiet "user@$uid.service"

as_dev() {
	runuser -u dev -- env HOME=/home/dev XDG_RUNTIME_DIR="/run/user/$uid" \
		DBUS_SESSION_BUS_ADDRESS="unix:path=/run/user/$uid/bus" "$@"
}

# The app behind every route.
as_dev python3 -m http.server 3000 --bind 127.0.0.1 --directory /tmp >/dev/null 2>&1 &

run() {
	mode=$1
	step "$mode: sb setup"
	as_dev sb setup --yes
	retry as_dev sb ls
	as_dev sb add probe 3000
	as_dev sb add myapp 3000

	openssl verify -CAfile /etc/ssl/certs/ca-certificates.crt "$ca" >/dev/null || fail "CA not in the system bundle"

	step "$mode: HTTPS with a trusted certificate"
	retry curl -fsS -o /dev/null https://probe.test/
	curl -fsS -o /dev/null https://myapp.test/ || fail "https://myapp.test"
	got=$(curl -sS -o /dev/null -w '%{http_version}' https://myapp.test/)
	[ "$got" = 2 ] || fail "HTTP version $got, want 2"
	got=$(curl -sS -o /dev/null -w '%{http_code} %{redirect_url}' http://myapp.test/x?y=1)
	[ "$got" = "307 https://myapp.test/x?y=1" ] || fail "redirect: $got"
	if [ "$mode" = resolved ]; then
		as_dev sb add '*.tenants' 3000
		curl -fsS -o /dev/null https://acme.tenants.test/ || fail "wildcard route"
	else
		grep -q '^127.0.0.1 myapp.test$' /etc/hosts || fail "myapp.test missing from /etc/hosts"
		# The wildcard route from the resolved run can't work from /etc/hosts,
		# and doctor must say so.
		out=$(as_dev sb doctor 2>&1) && fail "sb doctor passed with a wildcard route in hosts mode"
		echo "$out" | grep -q "^\[FAIL\] \*.tenants.test: names under it don't resolve" || fail "doctor did not flag the wildcard: $out"
		as_dev sb rm '*.tenants'
	fi

	step "$mode: sb doctor"
	as_dev sb doctor || fail "sb doctor reported failures"

	step "$mode: sb uninstall"
	as_dev sb uninstall --yes
	for path in /etc/systemd/resolved.conf.d/switchboard-test.conf \
		/etc/systemd/system/switchboard-helper.service \
		/home/dev/.config/systemd/user/switchboard.service \
		/usr/local/libexec/switchboard; do
		[ ! -e "$path" ] || fail "$path left behind"
	done
	! grep -q 'BEGIN Switchboard' /etc/hosts || fail "/etc/hosts block left behind"
	for f in /usr/local/share/ca-certificates/Switchboard* /etc/ssl/certs/Switchboard*; do
		if [ -e "$f" ] || [ -L "$f" ]; then fail "$f left behind"; fi
	done
	! openssl verify -CAfile /etc/ssl/certs/ca-certificates.crt "$ca" >/dev/null 2>&1 || fail "CA still in the system bundle"
	! curl -fsS -o /dev/null --max-time 5 https://probe.test/ 2>/dev/null || fail "https://probe.test still works"
	[ -z "$(ss -H -ltn 'sport = :443')" ] || fail "port 443 still bound"
}

run resolved

step "switching to the /etc/hosts fallback"
systemctl disable --now systemd-resolved >/dev/null 2>&1
systemctl mask systemd-resolved >/dev/null 2>&1
# Only /etc/hosts may answer .test now; some Docker hosts' upstream DNS
# resolves any .test name. Written in place: Docker bind-mounts the file.
echo 'nameserver 127.0.0.2' >/etc/resolv.conf
run hosts
