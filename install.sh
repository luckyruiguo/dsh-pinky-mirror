#!/bin/bash
# DSH Pinky Mirror —— 一键安装
#
# 做的事情：
#   1. 检查依赖（Go、ffmpeg 库、adb）
#   2. 编译独立版 Pinky（含画面流补丁）
#   3. 安装为 systemd 用户服务（与系统版 pinky 完全隔离）
#   4. 安装 DSH 侧边栏插件
#
# 用法：
#   ./install.sh              # 完整安装
#   ./install.sh --skip-dsh   # 只装 Pinky 服务，不动 DSH 配置
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
INSTALL_DIR="${PINKY_MIRROR_DIR:-$HOME/tools/pinky-standalone}"
PORT="${PINKY_MIRROR_PORT:-7466}"
DSH_PROFILE="${DSH_PROFILE:-web}"
SKIP_DSH=0

for arg in "$@"; do
  case "$arg" in
    --skip-dsh) SKIP_DSH=1 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
  esac
done

C_OK=$'\033[32m'; C_NO=$'\033[31m'; C_DIM=$'\033[2m'; C_N=$'\033[0m'
ok()   { echo "${C_OK}✓${C_N} $*"; }
bad()  { echo "${C_NO}✗${C_N} $*"; }
info() { echo "${C_DIM}·${C_N} $*"; }

echo
echo "═══ DSH Pinky Mirror 安装 ═══"
echo "  安装目录: $INSTALL_DIR"
echo "  画面端口: $PORT"
echo

# ---------- 1. 依赖检查 ----------
echo "── 1/5 检查依赖 ──"

GO_BIN=""
for c in go "$HOME/tools/go-toolchain/go/bin/go" /usr/local/go/bin/go; do
  if command -v "$c" >/dev/null 2>&1 || [ -x "$c" ]; then GO_BIN="$c"; break; fi
done
if [ -z "$GO_BIN" ]; then
  bad "找不到 Go（需要 1.27+）"
  echo "  安装: https://go.dev/dl/  或解压到 ~/tools/go-toolchain/"
  exit 1
fi
ok "Go: $("$GO_BIN" version | awk '{print $3}')"

# ffmpeg 运行库：优先系统包，其次仓库自带
FFMPEG_LIB=""
for d in /opt/pinky /usr/lib/x86_64-linux-gnu; do
  if [ -e "$d/libavcodec.so.63" ]; then FFMPEG_LIB="$d"; break; fi
done
if [ -z "$FFMPEG_LIB" ]; then
  if [ -d "$ROOT/prebuilt/lib" ]; then
    FFMPEG_LIB="$ROOT/prebuilt/lib"
  else
    bad "找不到 FFmpeg 9 运行库（libavcodec.so.63）"
    echo "  选项 A: 安装官方 Pinky（自带库）  dpkg -i pinky_*.deb"
    echo "  选项 B: 安装 ffmpeg 9 开发包"
    exit 1
  fi
fi
ok "FFmpeg 库: $FFMPEG_LIB"

if command -v adb >/dev/null 2>&1; then
  ok "adb: $(command -v adb)"
else
  bad "找不到 adb（连接手机必需）"
  echo "  安装: sudo apt install android-tools-adb"
  exit 1
fi

# ---------- 2. 编译 ----------
echo
echo "── 2/5 编译独立版 Pinky ──"
mkdir -p "$INSTALL_DIR/bin" "$INSTALL_DIR/lib"

info "编译中（首次约 2-3 分钟）…"
cd "$ROOT/pinky-fork"
export PATH="$(dirname "$GO_BIN"):$PATH"
export GOFLAGS="-mod=mod"
if ! go build -trimpath -ldflags "-s -w" -o "$INSTALL_DIR/bin/pinky" ./cmd/pinky 2>&1 | tail -5; then
  bad "编译失败"
  exit 1
fi
ok "二进制: $INSTALL_DIR/bin/pinky ($(du -h "$INSTALL_DIR/bin/pinky" | cut -f1))"

# 复制 FFmpeg 库（真实复制，不软链，保证独立）
for lib in libavcodec.so.63 libavformat.so.63 libavutil.so.61 libswscale.so.10; do
  [ -e "$FFMPEG_LIB/$lib" ] && cp -L "$FFMPEG_LIB/$lib" "$INSTALL_DIR/lib/" 2>/dev/null || true
done
ok "FFmpeg 库: $INSTALL_DIR/lib ($(du -sh "$INSTALL_DIR/lib" | cut -f1))"

# ---------- 3. systemd 服务 ----------
echo
echo "── 3/5 安装 systemd 服务 ──"
mkdir -p "$INSTALL_DIR/home"/{config,data,run} "$HOME/.config/systemd/user"

UNIT="$HOME/.config/systemd/user/pinky-standalone.service"
sed -e "s|@ROOT@|$INSTALL_DIR|g" -e "s|@PORT@|$PORT|g" \
  "$ROOT/systemd/pinky-standalone.service" > "$UNIT"
ok "服务文件: $UNIT"

systemctl --user daemon-reload
systemctl --user enable --now pinky-standalone.service
sleep 8

if systemctl --user is-active --quiet pinky-standalone.service; then
  ok "服务已启动"
else
  bad "服务启动失败，查看: journalctl --user -u pinky-standalone -n 30"
  exit 1
fi

# 等待画面流就绪
info "等待手机连接…"
for i in $(seq 1 30); do
  if curl -s --max-time 2 "http://127.0.0.1:$PORT/screen/health" 2>/dev/null | grep -q '"connected":true'; then
    ok "画面流已就绪"
    break
  fi
  sleep 1
done

# ---------- 4. DSH 插件 ----------
echo
echo "── 4/5 安装 DSH 侧边栏插件 ──"
if [ "$SKIP_DSH" = "1" ]; then
  info "已跳过（--skip-dsh）"
else
  PROFILE_DIR="$HOME/.dsh/profiles/$DSH_PROFILE"
  if [ ! -d "$PROFILE_DIR" ]; then
    bad "找不到 DSH profile: $PROFILE_DIR"
    info "跳过插件安装"
  else
    PLUGIN_SRC="$ROOT/dsh-plugin"
    PLUGIN_DST="$INSTALL_DIR/dsh-plugin"
    rm -rf "$PLUGIN_DST"
    cp -r "$PLUGIN_SRC" "$PLUGIN_DST"

    # 备份并写入 profile
    cp "$PROFILE_DIR/package.json" "$PROFILE_DIR/package.json.bak-pinky-$(date +%s)" 2>/dev/null || true

    python3 - "$PROFILE_DIR" "$PLUGIN_DST" <<'PY'
import json, sys, os
profile_dir, plugin_dir = sys.argv[1], sys.argv[2]
p = os.path.join(profile_dir, 'package.json')
d = json.load(open(p))
changed = False

deps = d.setdefault('dependencies', {})
if deps.get('dsh-plugin-pinky') != f'link:{plugin_dir}':
    deps['dsh-plugin-pinky'] = f'link:{plugin_dir}'
    changed = True

bundles = d.setdefault('dsh', {}).setdefault('profile', {}).setdefault('bundles', [])
if 'dsh-plugin-pinky' not in bundles:
    bundles.append('dsh-plugin-pinky')
    changed = True

if changed:
    json.dump(d, open(p, 'w'), indent=2, ensure_ascii=False)
    print('profile updated')
else:
    print('profile unchanged')
PY
    ok "profile 已配置"

    info "安装依赖（可能较慢）…"
    ( cd "$PROFILE_DIR" && CI=true pnpm install --no-frozen-lockfile >/tmp/pinky-pnpm.log 2>&1 ) \
      && ok "依赖已安装" || { bad "pnpm install 失败，见 /tmp/pinky-pnpm.log"; }
  fi
fi

# ---------- 5. 完成 ----------
echo
echo "── 5/5 完成 ──"
HEALTH=$(curl -s --max-time 3 "http://127.0.0.1:$PORT/screen/health" 2>/dev/null || echo '{}')
echo
echo "  画面流:  $HEALTH"
echo "  查看页:  http://127.0.0.1:$PORT/screen/view"
echo
echo "  下一步:"
echo "    1. 重启 DSH:  dsh web"
echo "    2. 刷新浏览器，右侧边栏「开始」页会出现「📱 手机」"
echo
echo "  管理命令:"
echo "    systemctl --user status  pinky-standalone"
echo "    systemctl --user restart pinky-standalone"
echo "    $INSTALL_DIR/../dsh-pinky-mirror/uninstall.sh   # 卸载"
echo
