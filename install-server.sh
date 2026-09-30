#!/bin/sh
# Dingzi 面板一键安装。仅依赖 POSIX sh 和系统自带工具。
set -eu
umask 077

REPO="oarw/dingzi"
BIN_NAME="dingzi-server"
BIN_DIR="/usr/local/bin"
CONF_DIR="/etc/dingzi"
DATA_DIR="/var/lib/dingzi"
SYSTEMD_DIR="/etc/systemd/system"
INIT_DIR="/etc/init.d"
LOG_DIR="/var/log"
SERVICE_USER="dingzi-server"
VERSION=""
ARG_LISTEN=""
ARG_SECURE_COOKIE=""
ARG_RETENTION=""
UNINSTALL=0

say() { printf '  %s\n' "$*"; }
die() { printf '\n错误: %s\n' "$*" >&2; exit 1; }
usage() {
  cat <<'EOF'
用法: install-server.sh [选项]

默认下载最新正式版，安装 Linux systemd / OpenRC 服务。
面板已包含网页和 SQLite，无需另装 Node.js、数据库或 Docker。

  --version vX.Y.Z          指定发布版本（可指定预发布标签）
  --listen ADDR            监听地址，默认 :8008，例如 127.0.0.1:8008
  --secure-cookie          HTTPS 反代部署时启用 Secure 会话 cookie
  --secure-cookie=false    恢复 HTTP 会话 cookie
  --retention-days N       数据保留天数，1–365，默认 30
  --uninstall              卸载程序和服务，保留数据、配置和服务用户
  -h, --help               显示帮助

重复运行即升级；未指定的设置沿用 /etc/dingzi/server.conf。
数据固定存放在 /var/lib/dingzi。升级前停服务并备份整个数据目录。
监听支持 IPv4 / IPv6 地址，端口为 1024–65535。
EOF
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version|--listen|--retention-days)
      [ "$#" -ge 2 ] && [ -n "$2" ] || die "$1 需要一个值"
      case "$1" in
        --version) VERSION="$2" ;;
        --listen) ARG_LISTEN="$2" ;;
        --retention-days) ARG_RETENTION="$2" ;;
      esac
      shift 2 ;;
    --secure-cookie|--secure-cookie=true) ARG_SECURE_COOKIE=true; shift ;;
    --secure-cookie=false) ARG_SECURE_COOKIE=false; shift ;;
    --uninstall) UNINSTALL=1; shift ;;
    -h|--help) usage; exit 0 ;;
    *) die "未知参数: $1（用 --help 查看帮助）" ;;
  esac
done

[ "$(id -u)" = 0 ] || die "请以 root 运行，需要安装程序并注册系统服务。"
[ "$(uname -s)" = Linux ] || die "一键服务安装仅支持 Linux；其他系统请下载单文件手动运行。"
detect_init() {
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    echo systemd
  elif command -v rc-service >/dev/null 2>&1 && command -v rc-update >/dev/null 2>&1; then
    echo openrc
  else
    die "没有检测到 systemd 或 OpenRC；请手动运行面板单文件。"
  fi
}
INIT="$(detect_init)"

if [ "$UNINSTALL" = 1 ]; then
  case "$INIT" in
    systemd)
      if [ -f "$SYSTEMD_DIR/$BIN_NAME.service" ]; then
        systemctl stop "$BIN_NAME" || die "停止服务失败，未卸载。"
        systemctl disable "$BIN_NAME" || die "禁用服务失败，未卸载。"
        rm -f "$SYSTEMD_DIR/$BIN_NAME.service"
        systemctl daemon-reload
      fi ;;
    openrc)
      if [ -f "$INIT_DIR/$BIN_NAME" ]; then
        if rc-service "$BIN_NAME" status >/dev/null 2>&1; then
          rc-service "$BIN_NAME" stop || die "停止服务失败，未卸载。"
        fi
        rc-update del "$BIN_NAME" default
        rm -f "$INIT_DIR/$BIN_NAME"
      fi ;;
  esac
  rm -f "$BIN_DIR/$BIN_NAME"
  say "已卸载。保留数据 ${DATA_DIR}、配置 $CONF_DIR/server.conf、日志及服务用户。"
  exit 0
fi

# Read only known literal values. Never source a configuration as root shell code.
LISTEN=":8008"
SECURE_COOKIE=false
RETENTION_DAYS=30
CONF="$CONF_DIR/server.conf"
if [ -f "$CONF" ]; then
  while IFS='=' read -r key value || [ -n "$key" ]; do
    case "$key" in
      ''|'#'*) ;;
      LISTEN) LISTEN="$value" ;;
      SECURE_COOKIE) SECURE_COOKIE="$value" ;;
      RETENTION_DAYS) RETENTION_DAYS="$value" ;;
      *) die "$CONF 包含未知设置 ${key}；请检查文件，未修改安装。" ;;
    esac
  done < "$CONF"
fi
[ -z "$ARG_LISTEN" ] || LISTEN="$ARG_LISTEN"
[ -z "$ARG_SECURE_COOKIE" ] || SECURE_COOKIE="$ARG_SECURE_COOKIE"
[ -z "$ARG_RETENTION" ] || RETENTION_DAYS="$ARG_RETENTION"
printf '%s\n' "$LISTEN" | grep -Eq '^([0-9.]*|\[[0-9a-fA-F:]+\]):[0-9]{4,5}$' \
  || die "无效监听地址；例 :8008、127.0.0.1:8008、[::1]:8008。"
PORT="${LISTEN##*:}"
[ "$PORT" -ge 1024 ] && [ "$PORT" -le 65535 ] || die "端口必须为 1024–65535。"
case "$SECURE_COOKIE" in true|false) ;; *) die "SECURE_COOKIE 必须为 true 或 false。" ;; esac
printf '%s\n' "$RETENTION_DAYS" | grep -Eq '^[1-9][0-9]{0,2}$' \
  && [ "$RETENTION_DAYS" -le 365 ] || die "保留天数必须为 1–365。"

case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  i386|i486|i586|i686) ARCH=386 ;;
  armv6*|armv7*|armhf|arm) ARCH=arm ;;
  riscv64) ARCH=riscv64 ;;
  *) die "不支持的架构: $(uname -m)" ;;
esac

if command -v curl >/dev/null 2>&1; then
  fetch() { curl -fsSL "$1" -o "$2"; }
elif command -v wget >/dev/null 2>&1; then
  fetch() { wget -qO "$2" "$1"; }
else
  die "请先安装 curl 或 wget。"
fi
TMP="$(mktemp -d)"
STAGED_BIN=""
cleanup() {
  [ -z "$STAGED_BIN" ] || rm -f "$STAGED_BIN"
  rm -rf "$TMP"
}
trap cleanup EXIT
trap 'exit 1' HUP INT TERM

if [ -z "$VERSION" ]; then
  fetch "https://api.github.com/repos/$REPO/releases/latest" "$TMP/release.json" \
    || die "查询最新正式版失败；可通过 --version 指定版本。"
  VERSION="$(sed -n 's/.*"tag_name" *: *"\([^"]*\)".*/\1/p' "$TMP/release.json" | head -1)"
fi
case "$VERSION" in ''|*[!a-zA-Z0-9._-]*) die "无效的发布标签。" ;; esac
ASSET="$BIN_NAME-linux-$ARCH"
BASE="https://github.com/$REPO/releases/download/$VERSION"
say "安装 $BIN_NAME ${VERSION}（linux/${ARCH}，${INIT}）"
fetch "$BASE/$ASSET" "$TMP/$BIN_NAME" || die "下载面板失败，原安装未修改。"
fetch "$BASE/checksums.txt" "$TMP/checksums.txt" || die "下载校验和失败，原安装未修改。"
WANT="$(awk -v asset="$ASSET" '$2 == asset { print $1 }' "$TMP/checksums.txt")"
printf '%s\n' "$WANT" | grep -Eq '^[0-9a-fA-F]{64}$' \
  && [ "$(printf '%s\n' "$WANT" | wc -l | tr -d ' ')" = 1 ] \
  || die "校验和中缺少唯一有效的 $ASSET 条目。"
if command -v sha256sum >/dev/null 2>&1; then
  GOT="$(sha256sum "$TMP/$BIN_NAME" | awk '{print $1}')"
elif command -v shasum >/dev/null 2>&1; then
  GOT="$(shasum -a 256 "$TMP/$BIN_NAME" | awk '{print $1}')"
else
  die "需要 sha256sum 或 shasum，无法跳过校验。"
fi
[ "$GOT" = "$(printf '%s' "$WANT" | tr 'A-F' 'a-f')" ] || die "SHA256 校验失败，原安装未修改。"

install -d -m 0755 "$BIN_DIR"
# Stage on the destination filesystem, so /tmp may be mounted noexec.
STAGED_BIN="$(mktemp "$BIN_DIR/.dingzi-server.XXXXXX")"
install -m 0755 "$TMP/$BIN_NAME" "$STAGED_BIN"
"$STAGED_BIN" --version || die "面板无法在本机运行，原程序与服务未替换。"

if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  if command -v useradd >/dev/null 2>&1; then
    useradd --system --user-group --home-dir "$DATA_DIR" --no-create-home --shell /bin/false "$SERVICE_USER"
  elif command -v adduser >/dev/null 2>&1; then
    # Alpine / BusyBox.
    addgroup -S "$SERVICE_USER"
    adduser -S -D -H -h "$DATA_DIR" -s /bin/false -G "$SERVICE_USER" "$SERVICE_USER"
  else
    die "找不到 useradd 或 adduser，无法创建独立服务用户。"
  fi
fi
[ "$(id -u "$SERVICE_USER")" != 0 ] || die "服务用户不能是 root。"
install -d -m 0700 "$CONF_DIR"
install -d -m 0700 "$DATA_DIR"
# The service creates and owns its data files. Never operate as root on child
# names it can replace with links, including during an upgrade.
chown "$SERVICE_USER:$SERVICE_USER" "$DATA_DIR"
printf 'LISTEN=%s\nSECURE_COOKIE=%s\nRETENTION_DAYS=%s\n' \
  "$LISTEN" "$SECURE_COOKIE" "$RETENTION_DAYS" > "$TMP/server.conf"
install -m 0600 "$TMP/server.conf" "$CONF"
mv -f "$STAGED_BIN" "$BIN_DIR/$BIN_NAME"
STAGED_BIN=""

case "$INIT" in
  systemd)
    cat > "$TMP/service" <<EOF
[Unit]
Description=Dingzi monitoring panel
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Group=$SERVICE_USER
WorkingDirectory=$DATA_DIR
ExecStart=$BIN_DIR/$BIN_NAME --data $DATA_DIR --listen $LISTEN --secure-cookie=$SECURE_COOKIE --retention-days $RETENTION_DAYS
Restart=on-failure
RestartSec=5s
TimeoutStopSec=30s
UMask=0077
NoNewPrivileges=true
PrivateTmp=true
ProtectHome=true
ProtectSystem=full

[Install]
WantedBy=multi-user.target
EOF
    install -m 0644 "$TMP/service" "$SYSTEMD_DIR/$BIN_NAME.service"
    systemctl daemon-reload
    systemctl enable "$BIN_NAME" || die "设置开机启动失败，请检查 systemctl status ${BIN_NAME}。"
    systemctl restart "$BIN_NAME" || die "启动失败，请检查 journalctl -u $BIN_NAME -n 50。"
    sleep 2
    systemctl is-active --quiet "$BIN_NAME" || die "服务未保持运行，请检查 journalctl -u $BIN_NAME -n 50。"
    ;;
  openrc)
    # The startup banner contains credentials; log files must not be public.
    touch "$LOG_DIR/$BIN_NAME.log"
    chmod 0600 "$LOG_DIR/$BIN_NAME.log"
    chown "$SERVICE_USER:$SERVICE_USER" "$LOG_DIR/$BIN_NAME.log"
    cat > "$TMP/service" <<EOF
#!/sbin/openrc-run
name="$BIN_NAME"
description="Dingzi monitoring panel"
supervisor="supervise-daemon"
command="$BIN_DIR/$BIN_NAME"
command_args="--data $DATA_DIR --listen '$LISTEN' --secure-cookie=$SECURE_COOKIE --retention-days $RETENTION_DAYS"
command_user="$SERVICE_USER:$SERVICE_USER"
directory="$DATA_DIR"
umask="0077"
respawn_delay=5
respawn_max=5
respawn_period=60
output_log="$LOG_DIR/$BIN_NAME.log"
error_log="$LOG_DIR/$BIN_NAME.log"
depend() {
  need net
}
EOF
    install -m 0755 "$TMP/service" "$INIT_DIR/$BIN_NAME"
    rc-update add "$BIN_NAME" default || die "设置开机启动失败。"
    rc-service "$BIN_NAME" restart || die "启动失败，请检查 $LOG_DIR/$BIN_NAME.log。"
    sleep 2
    rc-service "$BIN_NAME" status || die "服务未保持运行，请检查 $LOG_DIR/$BIN_NAME.log。"
    ;;
esac

printf '\n'
say "安装完成。监听 ${LISTEN}，数据 $DATA_DIR"
say "浏览器打开 http://<服务器 IP>:${PORT}（HTTPS 反代部署请使用你的域名）。"
say "首次管理员密码与 Agent 密钥请从服务日志中的启动横幅读取："
case "$INIT" in
  systemd) say "sudo journalctl -u $BIN_NAME --no-pager -n 50" ;;
  openrc) say "sudo tail -n 50 $LOG_DIR/$BIN_NAME.log" ;;
esac
say "再次运行本脚本可升级；未指定的监听、Cookie 和保留期设置会保留。"
