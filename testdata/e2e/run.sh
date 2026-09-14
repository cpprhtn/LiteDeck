#!/usr/bin/env bash
# End-to-end: the real frontend, the real Go, a real SSH server.
#
#     ./testdata/e2e/run.sh
#
# The gap this fills is that there are no frontend tests at all. Everything below
# the bindings is covered and everything above them is covered by looking at it,
# which is how a rename that moved a file to the wrong directory and an editor
# that saved through a symlink both shipped. Both were found by driving the app
# by hand; this is the same drive, written down.
#
# Nothing here is stubbed. It starts the demo container (a real Ubuntu with
# systemd, sshd, docker and a project tree), starts the headless server against
# an empty HOME, and drives it with a browser. A failure means the whole stack
# from the DOM to the SSH channel disagrees with what it did last time.
#
# Requirements: docker, node, and a Chrome or Chromium on the machine. The
# browser is not downloaded — playwright-core drives the one that is already
# installed, which is also what GitHub's runners have.
set -uo pipefail

REPO="$(cd "$(dirname "$0")/../.." && pwd)"
HERE="$REPO/testdata/e2e"
PORT="${LITEDECK_E2E_PORT:-18765}"
SSH_PORT="${LITEDECK_E2E_SSH_PORT:-2299}"
CONTAINER="${LITEDECK_E2E_CONTAINER:-litedeck-e2e}"
WORK="$(mktemp -d)"
KEEP="${LITEDECK_E2E_KEEP:-}"
# Screenshots survive the run by default only if somebody asks. The work
# directory goes at the end, and a failure screenshot inside it goes with it —
# which is exactly the run where the picture was wanted.
ARTIFACTS="${LITEDECK_E2E_ARTIFACTS:-$WORK}"
mkdir -p "$ARTIFACTS"

say() { printf '\n\033[1m%s\033[0m\n' "$*"; }

cleanup() {
	local rc=$?
	[ -n "${SERVER_PID:-}" ] && kill "$SERVER_PID" 2>/dev/null
	if [ -z "$KEEP" ]; then
		docker rm -f "$CONTAINER" >/dev/null 2>&1
		rm -rf "$WORK"
	else
		echo "kept: container $CONTAINER, work dir $WORK"
	fi
	exit $rc
}
trap cleanup EXIT INT TERM

# ── the server under test ────────────────────────────────────────────────
say "demo 서버 (docker)"
if ! docker image inspect litedeck-demo >/dev/null 2>&1; then
	docker build -t litedeck-demo "$REPO/testdata/demo" || exit 1
fi
docker rm -f "$CONTAINER" >/dev/null 2>&1
docker run -d --name "$CONTAINER" --privileged --cgroupns=host \
	-v /sys/fs/cgroup:/sys/fs/cgroup:rw -p "$SSH_PORT:22" litedeck-demo >/dev/null || exit 1
for _ in $(seq 1 60); do
	nc -z 127.0.0.1 "$SSH_PORT" 2>/dev/null && break
	sleep 1
done
nc -z 127.0.0.1 "$SSH_PORT" || { echo "sshd never came up on $SSH_PORT" >&2; exit 1; }
echo "  sshd on $SSH_PORT"

# ── an isolated HOME with one host in it ─────────────────────────────────
#
# The host list is written rather than added through the UI. Adding a host is
# one dialog; what this is here to exercise is everything after it, and a
# harness that spends its first thirty seconds filling in a form fails for
# reasons that have nothing to do with the change under test.
#
# A throwaway key rather than the password, so nothing has to be typed into a
# prompt to get as far as the first screen. The host key prompt is left in
# place — answering that one IS part of the path.
say "설정 (격리된 HOME)"
ssh-keygen -q -t ed25519 -N "" -f "$WORK/id_e2e" || exit 1
docker exec -i "$CONTAINER" bash -c '
	mkdir -p /home/deploy/.ssh &&
	cat >> /home/deploy/.ssh/authorized_keys &&
	chown -R deploy:deploy /home/deploy/.ssh &&
	chmod 700 /home/deploy/.ssh &&
	chmod 600 /home/deploy/.ssh/authorized_keys' < "$WORK/id_e2e.pub" || exit 1

# os.UserConfigDir, which is what the app asks, reads XDG_CONFIG_HOME on Linux
# and $HOME/Library/Application Support on macOS. Both are written so the same
# script works on a laptop and on a runner.
for cfg in "$WORK/config/litedeck" "$WORK/Library/Application Support/litedeck"; do
	mkdir -p "$cfg"
	cat > "$cfg/hosts.json" <<JSON
[{
  "id": "e2e",
  "name": "e2e demo",
  "hostname": "127.0.0.1",
  "port": $SSH_PORT,
  "user": "deploy",
  "auth": ["key"],
  "identityFile": "$WORK/id_e2e"
}]
JSON
done
echo "  $WORK/config/litedeck/hosts.json"

say "litedeck-server"
cd "$REPO"
go build -o "$WORK/litedeck-server" ./cmd/litedeck-server || exit 1
HOME="$WORK" XDG_CONFIG_HOME="$WORK/config" \
	"$WORK/litedeck-server" --addr "127.0.0.1:$PORT" --no-auth \
	>"$WORK/server.log" 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 40); do
	curl -fsS "http://127.0.0.1:$PORT/" >/dev/null 2>&1 && break
	sleep 0.5
done
curl -fsS "http://127.0.0.1:$PORT/" >/dev/null 2>&1 || {
	echo "server never answered on $PORT" >&2
	cat "$WORK/server.log" >&2
	exit 1
}
echo "  http://127.0.0.1:$PORT  (pid $SERVER_PID)"

# ── the browser ──────────────────────────────────────────────────────────
say "playwright"
cd "$HERE"
[ -d node_modules/playwright-core ] || npm install --no-save --silent playwright-core || exit 1

LITEDECK_E2E_URL="http://127.0.0.1:$PORT" \
LITEDECK_E2E_SSH_PORT="$SSH_PORT" \
LITEDECK_E2E_CONTAINER="$CONTAINER" \
LITEDECK_E2E_ARTIFACTS="$ARTIFACTS" \
	node "$HERE/e2e.mjs" 2>&1 | tee "$WORK/harness.log"
rc=${PIPESTATUS[0]}

if [ $rc -ne 0 ]; then
	say "서버 로그 (마지막 40줄)"
	tail -40 "$WORK/server.log"
	# Repeated at the very end, and last on purpose. A CI log gets read from the
	# bottom, and the first time this failed on a runner the tail everybody saw
	# was the server log — one line saying it had started listening, which is
	# true of every run including the ones that pass. What failed was forty lines
	# further up.
	say "실패한 검사"
	# The FAIL line and the indented detail under it, and nothing else.
	awk '/^FAIL/ { f = 1; print; next } f && /^[[:space:]]/ { print; next } { f = 0 }' \
		"$WORK/harness.log" | grep . ||
		echo "  (검사가 하나도 돌지 않았다 — 위의 node 출력을 볼 것)"
fi
exit $rc
