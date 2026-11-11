#!/bin/bash
# DSH Pinky Mirror —— 卸载
#
# 停止并移除服务、DSH 插件配置，可选删除安装目录。
set -uo pipefail

INSTALL_DIR="${PINKY_MIRROR_DIR:-$HOME/tools/pinky-standalone}"
DSH_PROFILE="${DSH_PROFILE:-web}"
PROFILE_DIR="$HOME/.dsh/profiles/$DSH_PROFILE"

C_OK=$'\033[32m'; C_DIM=$'\033[2m'; C_N=$'\033[0m'
ok()   { echo "${C_OK}✓${C_N} $*"; }
info() { echo "${C_DIM}·${C_N} $*"; }

echo
echo "═══ DSH Pinky Mirror 卸载 ═══"
echo

# 1. 停服务
if systemctl --user list-unit-files 2>/dev/null | grep -q pinky-standalone.service; then
  systemctl --user stop pinky-standalone.service 2>/dev/null
  systemctl --user disable pinky-standalone.service 2>/dev/null
  rm -f "$HOME/.config/systemd/user/pinky-standalone.service"
  systemctl --user daemon-reload
  ok "服务已移除"
else
  info "没有找到服务"
fi

# 2. 移除 DSH 插件
if [ -f "$PROFILE_DIR/package.json" ]; then
  cp "$PROFILE_DIR/package.json" "$PROFILE_DIR/package.json.bak-pinky-uninstall-$(date +%s)" 2>/dev/null

  python3 - "$PROFILE_DIR" <<'PY'
import json, sys, os
p = os.path.join(sys.argv[1], 'package.json')
d = json.load(open(p))
changed = False

deps = d.get('dependencies', {})
if 'dsh-plugin-pinky' in deps:
    del deps['dsh-plugin-pinky']
    changed = True

bundles = d.get('dsh', {}).get('profile', {}).get('bundles', [])
if 'dsh-plugin-pinky' in bundles:
    bundles.remove('dsh-plugin-pinky')
    changed = True

if changed:
    json.dump(d, open(p, 'w'), indent=2, ensure_ascii=False)
    print('plugin removed from profile')
else:
    print('plugin not present')
PY
  ok "DSH 插件配置已移除"
fi

# 3. 删安装目录
if [ -d "$INSTALL_DIR" ]; then
  read -r -p "删除安装目录 $INSTALL_DIR ? [y/N] " ans
  if [ "${ans,,}" = "y" ]; then
    rm -rf "$INSTALL_DIR"
    ok "已删除 $INSTALL_DIR"
  else
    info "保留 $INSTALL_DIR"
  fi
fi

echo
echo "完成。重启 DSH 生效：dsh web"
echo
