#!/bin/sh
# dingzi-agent 一键安装脚本
#
#   curl -fsSL https://raw.githubusercontent.com/oarw/dingzi/main/install.sh | sh -s -- \
#       --server https://panel.example.com --secret <面板打印的密钥>
#
# 为什么是 POSIX sh 而不是 bash：Alpine 和各种 slim 镜像没有 bash，而那些正是
# 最需要一键安装的机器。写 #!/bin/bash 会在它们上面一行都跑不了。
#
# 为什么校验和是强制的、没有 --skip-checksum：这个脚本以 root 身份执行一个刚从
# 网上下载的二进制。一个"跳过校验"的开关迟早会有人在生产上用。没有这个开关，
# 就没有人能用。
set -eu

REPO="oarw/dingzi"
BIN_DIR="/usr/local/bin"
CONF_DIR="/etc/dingzi"
BIN_NAME="dingzi-agent"

SERVER=""
SECRET=""
VERSION=""
ALLOW_TERMINAL=0
UNINSTALL=0

usage() {
  cat <<'EOF'
用法: install.sh --server <面板地址> --secret <密钥> [选项]

必需:
  --server URL        面板地址，如 https://panel.example.com
  --secret KEY        面板首次启动时打印的 Agent 密钥

选项:
  --version vX.Y.Z    安装指定版本（默认最新正式版，不含预发布）
  --allow-terminal    允许面板在这台机器上打开 shell（默认关闭）
  --uninstall         卸载 agent（保留配置文件）
  -h, --help          显示本帮助

关于 --allow-terminal：
  打开后面板可以在这台机器上开一个 shell，以 agent 进程的用户身份运行。
  本脚本把 agent 装成 root 服务，所以那会是一个 root shell。
  默认关闭，需要你显式打开 —— 开关放在被开终端的这台机器上，而不是只放在
  那个可能被攻破的面板上。
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --server)         [ $# -ge 2 ] || { echo "--server 需要一个值" >&2; exit 1; }; SERVER="$2"; shift 2 ;;
    --secret)         [ $# -ge 2 ] || { echo "--secret 需要一个值" >&2; exit 1; }; SECRET="$2"; shift 2 ;;
    --version)        [ $# -ge 2 ] || { echo "--version 需要一个值" >&2; exit 1; }; VERSION="$2"; shift 2 ;;
    --allow-terminal) ALLOW_TERMINAL=1; shift ;;
    --uninstall)      UNINSTALL=1; shift ;;
    -h|--help)        usage; exit 0 ;;
    *)                echo "未知参数: $1" >&2; usage >&2; exit 1 ;;
  esac
done

say()  { printf '  %s\n' "$*"; }
die()  { printf '\n错误: %s\n' "$*" >&2; exit 1; }

[ "$(id -u)" = "0" ] || die "需要 root 权限（要写 $BIN_DIR 和 $CONF_DIR，并注册服务）
  请用 sudo 重新运行。"

# ---- 服务管理器 -------------------------------------------------------------
# Alpine 用 OpenRC 不用 systemd，而 Alpine 正是这个脚本要照顾的场景。
detect_init() {
  if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
    echo systemd
  elif command -v rc-update >/dev/null 2>&1; then
    echo openrc
  else
    echo none
  fi
}
INIT="$(detect_init)"

# ---- 卸载 -------------------------------------------------------------------
if [ "$UNINSTALL" = "1" ]; then
  say "正在卸载 $BIN_NAME ..."
  case "$INIT" in
    systemd)
      systemctl stop "$BIN_NAME" 2>/dev/null || true
      systemctl disable "$BIN_NAME" 2>/dev/null || true
      rm -f "/etc/systemd/system/$BIN_NAME.service"
      systemctl daemon-reload 2>/dev/null || true
      ;;
    openrc)
      rc-service "$BIN_NAME" stop 2>/dev/null || true
      rc-update del "$BIN_NAME" default 2>/dev/null || true
      rm -f "/etc/init.d/$BIN_NAME"
      ;;
  esac
  rm -f "$BIN_DIR/$BIN_NAME"
  # 配置故意留下：里面存着这台机器的 uuid。删掉它，重装后面板会把这台机器
  # 当成一台新机器，历史就断了。
  say "已卸载。配置保留在 $CONF_DIR（含机器 uuid，重装可续用）"
  say "要彻底清除：rm -rf $CONF_DIR"
  exit 0
fi

[ -n "$SERVER" ] || die "缺少 --server
  例：--server https://panel.example.com"
[ -n "$SECRET" ] || die "缺少 --secret
  密钥在面板首次启动的横幅里打印。"

# ---- 平台探测 ---------------------------------------------------------------
OS="$(uname -s | tr 'A-Z' 'a-z')"
case "$OS" in
  linux)            GOOS=linux ;;
  darwin)           GOOS=darwin ;;
  freebsd)          GOOS=freebsd ;;
  *cygwin*|*mingw*|*msys*) die "Windows 请下载 exe 手动注册服务，本脚本不适用" ;;
  *)                die "不支持的系统: $OS" ;;
esac

RAW_ARCH="$(uname -m)"
case "$RAW_ARCH" in
  x86_64|amd64)      GOARCH=amd64 ;;
  aarch64|arm64)     GOARCH=arm64 ;;
  i386|i486|i586|i686) GOARCH=386 ;;
  riscv64)           GOARCH=riscv64 ;;
  # armv6 / armv7 / armhf 都用同一个 arm 构建：GOARCH=arm 默认按 ARMv6 编译，
  # 在 ARMv7 上也能跑。反过来不行，所以只出一个。
  armv6*|armv7*|armhf|arm) GOARCH=arm ;;
  *)                 die "不支持的架构: $RAW_ARCH" ;;
esac

# 虚拟化类型只用于诊断输出。agent 对 KVM / LXC / OpenVZ / Docker 的处理没有
# 区别（都只是读 /proc 和 /sys），但装完知道自己在哪种环境里，排查时省一轮问答。
# OpenVZ 尤其值得报出来：它经常隐藏或伪造部分指标。
detect_virt() {
  if command -v systemd-detect-virt >/dev/null 2>&1; then
    v="$(systemd-detect-virt 2>/dev/null || true)"
    [ -n "$v" ] && [ "$v" != "none" ] && { echo "$v"; return; }
  fi
  [ -f /.dockerenv ] && { echo docker; return; }
  [ -d /proc/vz ] && [ ! -d /proc/bc ] && { echo openvz; return; }
  if [ -r /proc/1/environ ] && tr '\0' '\n' < /proc/1/environ 2>/dev/null | grep -q '^container='; then
    tr '\0' '\n' < /proc/1/environ | sed -n 's/^container=//p' | head -1
    return
  fi
  if [ -r /proc/cpuinfo ] && grep -qi 'hypervisor' /proc/cpuinfo 2>/dev/null; then
    echo "vm"; return
  fi
  echo "physical/unknown"
}
VIRT="$(detect_virt)"

# ---- 下载工具 ---------------------------------------------------------------
if command -v curl >/dev/null 2>&1; then
  fetch()      { curl -fsSL "$1" -o "$2"; }
  fetch_out()  { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
  fetch()      { wget -qO "$2" "$1"; }
  fetch_out()  { wget -qO- "$1"; }
else
  die "需要 curl 或 wget，两个都没有找到
  Alpine: apk add --no-cache curl
  Debian: apt-get install -y curl"
fi

# ---- 版本 -------------------------------------------------------------------
if [ -z "$VERSION" ]; then
  say "查询最新版本 ..."
  # /releases/latest 会自动跳过 prerelease —— PR 构建出来的预发布版不该被
  # 一键脚本装到生产机器上。用 grep+sed 而不是 jq：最小镜像里没有 jq，为了
  # 装 agent 先装 jq 就把"一行装好"这件事毁了。
  VERSION="$(fetch_out "https://api.github.com/repos/$REPO/releases/latest" \
    | grep -m1 '"tag_name"' | sed 's/.*"tag_name" *: *"\([^"]*\)".*/\1/')" || true
  [ -n "$VERSION" ] || die "查不到最新版本
  可以手动指定：--version v0.1.0
  或看一眼 https://github.com/$REPO/releases"
fi

ASSET="${BIN_NAME}-${GOOS}-${GOARCH}"
BASE="https://github.com/$REPO/releases/download/$VERSION"

printf '\n  dingzi-agent 安装\n'
say "版本      $VERSION"
say "平台      $GOOS/$GOARCH  (uname -m: $RAW_ARCH)"
say "虚拟化    $VIRT"
say "服务管理  $INIT"
printf '\n'

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT INT TERM

say "下载 $ASSET ..."
fetch "$BASE/$ASSET" "$TMP/$BIN_NAME" \
  || die "下载失败: $BASE/$ASSET
  这个版本可能没有 $GOOS/$GOARCH 的构建产物。"

# ---- 校验和（强制） ---------------------------------------------------------
say "下载校验和 ..."
fetch "$BASE/checksums.txt" "$TMP/checksums.txt" \
  || die "这个版本没有 checksums.txt，拒绝安装。
  本脚本以 root 执行下载来的二进制，不做校验就执行是不可接受的。"

if   command -v sha256sum >/dev/null 2>&1; then GOT="$(sha256sum "$TMP/$BIN_NAME" | cut -d' ' -f1)"
elif command -v shasum    >/dev/null 2>&1; then GOT="$(shasum -a 256 "$TMP/$BIN_NAME" | cut -d' ' -f1)"
elif command -v openssl   >/dev/null 2>&1; then GOT="$(openssl dgst -sha256 "$TMP/$BIN_NAME" | sed 's/.*= *//')"
else
  die "找不到 sha256sum / shasum / openssl，无法校验，拒绝安装。
  Alpine: apk add --no-cache coreutils"
fi

# Anchored to end of line. An unanchored match for "dingzi-agent-linux-arm"
# also matches the "...-arm64" line, and whichever comes first wins — so a
# Raspberry Pi would be handed the arm64 hash and refuse a perfectly good
# binary. Fails closed rather than open, but it would break every armv7 install.
WANT="$(grep -E " ${ASSET}$" "$TMP/checksums.txt" | head -1 | cut -d' ' -f1)"
[ -n "$WANT" ] || die "checksums.txt 里没有 $ASSET 的条目，拒绝安装。"
[ "$GOT" = "$WANT" ] || die "校验和不匹配，拒绝安装。
  期望 $WANT
  实际 $GOT
  可能是下载被截断，也可能是被替换过。"
say "校验通过 ${GOT}"

# ---- 安装 -------------------------------------------------------------------
install -d -m 0755 "$BIN_DIR"
install -m 0755 "$TMP/$BIN_NAME" "$BIN_DIR/$BIN_NAME"
install -d -m 0700 "$CONF_DIR"

CONF="$CONF_DIR/agent.yaml"
if [ -f "$CONF" ]; then
  # 保留 uuid，但更新 server / secret。
  #
  # uuid 必须留：它是这台机器的身份，覆盖掉等于在面板上变成一台新机器，历史
  # 断在这里。但把用户这次显式传进来的 server / secret 丢掉同样不对 —— 那是
  # 一个静默失败：命令看起来成功了，agent 却还在连旧面板。所以两者都要。
  say "保留已有 uuid，更新 server / secret"
  OLD_UUID="$(sed -n 's/^uuid: *"\{0,1\}\([^"]*\)"\{0,1\}$/\1/p' "$CONF" | head -1)"
  umask 077
  {
    printf '# 由 install.sh 生成\n'
    printf 'server: %s\n' "$SERVER"
    printf 'secret: %s\n' "$SECRET"
    printf 'uuid: "%s"\n' "$OLD_UUID"
    [ "$ALLOW_TERMINAL" = "1" ] && printf 'allow_terminal: true\n'
  } > "$CONF.new"
  mv "$CONF.new" "$CONF"
  chmod 0600 "$CONF"
  if [ -n "$OLD_UUID" ]; then
    say "  uuid 保持 $OLD_UUID"
  fi
  # allow_terminal 只在这次带了 --allow-terminal 时才写。不带就是关掉 ——
  # 一个安全开关不该因为"上次开过"而继续开着，那样就没人知道它现在是什么状态。
  if [ "$ALLOW_TERMINAL" = "1" ]; then
    say "  网页终端: 开启"
  else
    say "  网页终端: 关闭（要开请加 --allow-terminal）"
  fi
else
  umask 077
  cat > "$CONF" <<EOF
# 由 install.sh 生成
server: $SERVER
secret: $SECRET
uuid: ""
EOF
  if [ "$ALLOW_TERMINAL" = "1" ]; then
    printf 'allow_terminal: true\n' >> "$CONF"
  fi
  chmod 0600 "$CONF"
  say "已写入配置 $CONF (0600)"
fi

# ---- 服务 -------------------------------------------------------------------
case "$INIT" in
  systemd)
    # 刻意不加 NoNewPrivileges / ProtectHome / PrivateTmp：那些会让网页终端里的
    # su、sudo 和进 home 目录静默失败。终端本来就是显式打开的 root shell，
    # 加一层挡不住它、只会让它以难以诊断的方式坏掉。
    cat > "/etc/systemd/system/$BIN_NAME.service" <<EOF
[Unit]
Description=Dingzi monitoring agent
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=$BIN_DIR/$BIN_NAME --config $CONF
Restart=always
RestartSec=5s
# agent 掉线会自己指数退避重连，systemd 只负责进程真的死掉的情况。
StartLimitBurst=0

[Install]
WantedBy=multi-user.target
EOF
    systemctl daemon-reload
    systemctl enable "$BIN_NAME" >/dev/null 2>&1 || true
    systemctl restart "$BIN_NAME"
    say "systemd 服务已启动"
    ;;

  openrc)
    cat > "/etc/init.d/$BIN_NAME" <<EOF
#!/sbin/openrc-run
name="$BIN_NAME"
description="Dingzi monitoring agent"
command="$BIN_DIR/$BIN_NAME"
command_args="--config $CONF"
command_background=true
pidfile="/run/\$RC_SVCNAME.pid"
output_log="/var/log/$BIN_NAME.log"
error_log="/var/log/$BIN_NAME.log"

depend() {
    need net
}
EOF
    chmod 0755 "/etc/init.d/$BIN_NAME"
    rc-update add "$BIN_NAME" default >/dev/null 2>&1 || true
    rc-service "$BIN_NAME" restart
    say "OpenRC 服务已启动"
    ;;

  none)
    say "没有检测到 systemd 或 OpenRC，跳过服务注册。"
    say "手动运行：$BIN_DIR/$BIN_NAME --config $CONF"
    ;;
esac

printf '\n  安装完成\n'
say "二进制  $BIN_DIR/$BIN_NAME"
say "配置    $CONF"
case "$INIT" in
  systemd) say "日志    journalctl -u $BIN_NAME -f" ;;
  openrc)  say "日志    tail -f /var/log/$BIN_NAME.log" ;;
esac
if [ "$ALLOW_TERMINAL" = "1" ]; then
  printf '\n'
  say "网页终端已开启 —— 面板可以在这台机器上开 root shell。"
fi
printf '\n'
