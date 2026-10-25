#!/usr/bin/env bash
# PermaPorter 卸载脚本（Debian/Ubuntu）：与 install.sh 对称，一键卸载清理。
# 默认保留数据目录（单库 + 四棵存储树 + 配置文件，重装可无缝续用）；
# 追加 --purge 将数据目录与环境文件中自定义的 CONFIG_DIR 一并删除。
# 注意：*_STORAGE_DIR / DB_DSN 指向数据目录之外的自定义路径不在清理范围，需手动处理。
# 用法: sudo ./uninstall.sh [--purge]
set -euo pipefail

SERVICE_NAME=permaporter
BIN=/usr/local/bin/permaporter
ENV_FILE=/etc/default/permaporter
UNIT_FILE=/etc/systemd/system/${SERVICE_NAME}.service
DATA_DIR=/var/lib/permaporter

PURGE=0
case "${1:-}" in
  "") ;;
  --purge) PURGE=1 ;;
  *) echo "用法: sudo ./uninstall.sh [--purge]"; exit 1 ;;
esac

[ "$(id -u)" -eq 0 ] || { echo "请用 sudo 运行"; exit 1; }

# 环境文件中自定义的配置目录（仅取未注释行，与 systemd EnvironmentFile 口径一致）
CONFIG_DIR_CUSTOM=""
if [ -f "$ENV_FILE" ]; then
  CONFIG_DIR_CUSTOM="$(grep -E '^CONFIG_DIR=' "$ENV_FILE" | tail -1 | cut -d= -f2- \
    | sed -e 's/^"//' -e 's/"$//' -e "s/^'//" -e "s/'\$//" || true)"
fi

echo "==> 停止并注销服务"
systemctl stop "$SERVICE_NAME" 2>/dev/null || true
systemctl disable "$SERVICE_NAME" 2>/dev/null || true
if [ -f "$UNIT_FILE" ]; then
  rm -f "$UNIT_FILE"
  systemctl daemon-reload
fi
systemctl reset-failed "$SERVICE_NAME" 2>/dev/null || true

echo "==> 移除二进制与环境配置"
rm -f "$BIN" "$ENV_FILE"

echo "==> 移除系统用户"
if id -u permaporter &>/dev/null; then
  userdel permaporter 2>/dev/null || echo "    用户 permaporter 删除失败（仍有进程占用时可稍后手动 userdel permaporter）"
fi
groupdel permaporter 2>/dev/null || true

if [ "$PURGE" -eq 1 ]; then
  echo "==> 删除数据目录（单库 + 四棵存储树 + 配置文件）"
  rm -rf "$DATA_DIR"
  if [ -n "$CONFIG_DIR_CUSTOM" ]; then
    # 防御：根目录、顶层系统目录与已随数据目录删除的路径跳过
    case "$CONFIG_DIR_CUSTOM" in
      /|/etc|/usr|/var|/var/lib|"$DATA_DIR") echo "    自定义 CONFIG_DIR=$CONFIG_DIR_CUSTOM 不单独删除" ;;
      *) rm -rf "$CONFIG_DIR_CUSTOM" ;;
    esac
  fi
  echo "卸载完成：服务、二进制、配置与数据均已清除。"
else
  echo "==> 数据目录保留：$DATA_DIR"
  echo "    （单库 + 四棵存储树 + 配置文件；重装后可无缝续用，确认不再需要时手动删除）"
  echo "    一并清除请使用：sudo ./uninstall.sh --purge"
  echo "卸载完成：服务、二进制与环境配置已移除，数据已保留。"
fi
