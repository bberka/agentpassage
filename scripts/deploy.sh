#!/bin/sh
# Install the current commit and verify that the running service uses it.
# Linux with the systemd user service. Run through `make deploy`.
#   APX_URL         local gateway (default http://127.0.0.1:8787)
#   APX_PUBLIC_URL  optional public address to check as well
set -eu
cd "$(dirname "$0")/.."
url="${APX_URL:-http://127.0.0.1:8787}"
fail() { echo "deploy: $*" >&2; exit 1; }

[ -z "$(git status --porcelain)" ] || fail "uncommitted changes; commit them first"
git fetch -q
[ -z "$(git rev-list '@{upstream}..HEAD')" ] || fail "unpushed commits; push them first"
commit=$(git rev-parse --short HEAD)

make --no-print-directory build
# A config the new binary cannot parse would stop the service from starting.
./apx list >/dev/null || fail "the new build cannot load the config; nothing was installed"
make --no-print-directory install
systemctl --user restart apx.service

# healthy URL: succeed once URL/healthz reports this commit.
healthy() {
	tries=0
	while [ "$tries" -lt 20 ]; do
		curl -fsS --max-time 5 "$1/healthz" 2>/dev/null | grep -q "\"commit\": *\"$commit\"" && return 0
		tries=$((tries + 1))
		sleep 1
	done
	return 1
}
healthy "$url" || fail "$url is not serving $commit; see: systemctl --user status apx.service"
echo "deploy: $url serves $commit"
if [ -n "${APX_PUBLIC_URL:-}" ]; then
	healthy "$APX_PUBLIC_URL" || fail "$APX_PUBLIC_URL is not serving $commit"
	echo "deploy: $APX_PUBLIC_URL serves $commit"
fi

# Processes started before the install still run the old binary.
stale=""
for pid in $(pgrep -x apx || true); do
	case "$(readlink "/proc/$pid/exe" 2>/dev/null)" in
	*"(deleted)") stale="$stale $pid" ;;
	esac
done
if [ -n "$stale" ]; then
	echo "deploy: old processes still running (restart their host app or TUI):$stale"
fi
