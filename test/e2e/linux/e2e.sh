#!/bin/sh
# Runs the Linux end-to-end test in a privileged container that boots
# systemd: builds sb for the container, starts it, runs run.sh inside, and
# prints the service logs if it fails. It changes nothing on the host except
# Docker state, which it cleans up (set E2E_KEEP=1 to keep the container for
# debugging).
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
name=sb-e2e-$$

case "$(docker info --format '{{.Architecture}}')" in
x86_64 | amd64) arch=amd64 ;;
aarch64 | arm64) arch=arm64 ;;
*) echo "e2e: unsupported Docker architecture" >&2 && exit 1 ;;
esac

bin=$(mktemp -d)
cleanup() {
	if [ "${E2E_KEEP:-}" = 1 ]; then
		echo "e2e: kept container $name; remove it with: docker rm -f $name" >&2
	else
		docker rm -f "$name" >/dev/null 2>&1 || true
	fi
	rm -rf "$bin"
}
trap cleanup EXIT

(cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -o "$bin/sb" ./cmd/sb)
docker build -q -t sb-e2e "$here" >/dev/null
docker run -d --name "$name" --privileged --cgroupns=host \
	-v /sys/fs/cgroup:/sys/fs/cgroup:rw --tmpfs /run --tmpfs /run/lock \
	-v "$bin/sb:/usr/local/bin/sb:ro" sb-e2e >/dev/null

if docker exec "$name" sh /e2e/run.sh; then
	echo "e2e: PASS"
else
	status=$?
	echo "e2e: FAIL; service logs follow" >&2
	docker exec "$name" journalctl --no-pager -n 200 -u switchboard-helper.service -u systemd-resolved.service >&2 || true
	docker exec "$name" sh -c 'journalctl --no-pager -n 200 _UID=$(id -u dev)' >&2 || true
	exit "$status"
fi
