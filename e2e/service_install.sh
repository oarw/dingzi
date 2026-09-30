#!/bin/sh
# Destructive service integration test: disposable Linux CI hosts only.
set -eu
umask 077
[ "${DINGZI_SERVICE_TEST:-}" = 1 ] && [ "$(id -u)" = 0 ] || {
  echo 'Requires root and DINGZI_SERVICE_TEST=1 on a disposable Linux host.' >&2
  exit 1
}
BINARY="$1"
MANAGER="$2"
AGENT="$3"
AGENT_PID=""
INSTALLER="$(cd "$(dirname "$0")/.." && pwd)/install-server.sh"
for path in /usr/local/bin/dingzi-server /etc/dingzi /var/lib/dingzi /var/log/dingzi-server.log /etc/systemd/system/dingzi-server.service /etc/init.d/dingzi-server; do
  [ ! -e "$path" ] || { echo "Refusing to replace existing $path" >&2; exit 1; }
done
WORK="$(mktemp -d)"
REAL_CURL="$(command -v curl)"
cleanup() {
  if [ -n "$AGENT_PID" ]; then kill "$AGENT_PID" 2>/dev/null || true; wait "$AGENT_PID" 2>/dev/null || true; fi
  sh "$INSTALLER" --uninstall >/dev/null 2>&1 || true
  rm -rf "$WORK" /etc/dingzi /var/lib/dingzi
  rm -f /var/log/dingzi-server.log
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM
mkdir "$WORK/downloads" "$WORK/bin"
cp "$BINARY" "$WORK/downloads/dingzi-server-linux-amd64"
(cd "$WORK/downloads" && sha256sum dingzi-server-linux-amd64 > checksums.txt)
cat > "$WORK/bin/curl" <<'EOF'
#!/bin/sh
[ "$1" = -fsSL ] && [ "$3" = -o ] || exit 1
case "$2" in
  https://github.com/oarw/dingzi/releases/download/ci-test/dingzi-server-linux-amd64) name=dingzi-server-linux-amd64 ;;
  https://github.com/oarw/dingzi/releases/download/ci-test/checksums.txt) name=checksums.txt ;;
  *) exit 1 ;;
esac
cp "$DINGZI_TEST_DOWNLOADS/$name" "$4"
EOF
chmod 0755 "$WORK/bin/curl"
export DINGZI_TEST_DOWNLOADS="$WORK/downloads"
export PATH="$WORK/bin:$PATH"

stop_panel() {
  case "$MANAGER" in
    systemd) systemctl stop dingzi-server ;;
    openrc) rc-service dingzi-server stop ;;
    *) exit 1 ;;
  esac
}
check_panel() {
  ready=0
  for attempt in 1 2 3 4 5 6 7 8 9 10; do
    if "$REAL_CURL" -fsS --max-time 2 http://127.0.0.1:18008/api/v1/session > "$WORK/session.json"; then
      ready=1
      break
    fi
    sleep 1
  done
  [ "$ready" = 1 ] || { echo 'Panel did not become ready.' >&2; exit 1; }
  grep -q '"authed":false' "$WORK/session.json"
  "$REAL_CURL" -fsS --max-time 2 http://127.0.0.1:18008/ > "$WORK/page.html"
  grep -qi '<html' "$WORK/page.html"
  [ -s /var/lib/dingzi/config.yaml ] && [ -s /var/lib/dingzi/dingzi.db ]
  [ "$(stat -c %a /var/lib/dingzi/config.yaml)" = 600 ]
  [ "$(stat -c %U /var/lib/dingzi/config.yaml)" = dingzi-server ]
  uid="$(id -u dingzi-server)"
  [ "$uid" != 0 ]
  case "$MANAGER" in
    systemd)
      systemctl is-enabled --quiet dingzi-server
      pid="$(systemctl show dingzi-server --property MainPID --value)"
      grep -Eq "^Uid:[[:space:]]+$uid[[:space:]]" "/proc/$pid/status"
      ;;
    openrc)
      rc-service dingzi-server status
      [ -L /etc/runlevels/default/dingzi-server ]
      [ "$(stat -c %a /var/log/dingzi-server.log)" = 600 ]
      # Only the panel process is expected to run under this dedicated user.
      pgrep -u "$uid" -x dingzi-server >/dev/null
      ;;
  esac
}

sh "$INSTALLER" --version ci-test --listen 127.0.0.1:18008 --secure-cookie --retention-days 7
check_panel
key="$(sed -n 's/^agent_secret: *//p' /var/lib/dingzi/config.yaml)"
"$AGENT" --config "$WORK/agent.yaml" --server http://127.0.0.1:18008 --secret "$key" --name service-test > "$WORK/agent.log" 2>&1 &
AGENT_PID=$!
reported=0
for attempt in 1 2 3 4 5 6 7 8 9 10; do
  "$REAL_CURL" -fsS --max-time 2 http://127.0.0.1:18008/api/v1/public/servers > "$WORK/fleet.json"
  if grep -q '"online":true' "$WORK/fleet.json" && ! grep -q '"mem":null' "$WORK/fleet.json"; then reported=1; break; fi
  sleep 1
done
[ "$reported" = 1 ] || { echo 'Agent failed to report real metrics.' >&2; exit 1; }
kill "$AGENT_PID"
wait "$AGENT_PID"
AGENT_PID=""
cp /var/lib/dingzi/config.yaml "$WORK/original-config.yaml"
cp /etc/dingzi/server.conf "$WORK/original-server.conf"
stop_panel
cp /var/lib/dingzi/dingzi.db "$WORK/original.db"
# Upgrade with no settings supplied, including a stopped service.
sh "$INSTALLER" --version ci-test
check_panel
cmp /var/lib/dingzi/config.yaml "$WORK/original-config.yaml"
cmp /etc/dingzi/server.conf "$WORK/original-server.conf"
stop_panel
cmp /var/lib/dingzi/dingzi.db "$WORK/original.db"
sh "$INSTALLER" --uninstall
[ ! -e /usr/local/bin/dingzi-server ]
cmp /var/lib/dingzi/config.yaml "$WORK/original-config.yaml"
cmp /var/lib/dingzi/dingzi.db "$WORK/original.db"
cmp /etc/dingzi/server.conf "$WORK/original-server.conf"
# Reinstallation must reuse credentials and data retained by uninstall.
sh "$INSTALLER" --version ci-test
check_panel
cmp /var/lib/dingzi/config.yaml "$WORK/original-config.yaml"
printf 'PASS: %s install, non-root HTTP/UI, real agent metrics, upgrade, uninstall, reinstall\n' "$MANAGER"
