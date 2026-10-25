#!/usr/bin/env bash
# PermaPorter 安装脚本（Debian/Ubuntu）：二进制 + systemd + env 五端口变量
# 用法: sudo ./install.sh [tarball 目录]
set -euo pipefail

SRC_DIR="${1:-$(cd "$(dirname "$0")" && pwd)}"
SERVICE_NAME=permaporter
BIN=/usr/local/bin/permaporter
ENV_FILE=/etc/default/permaporter
DATA_DIR=/var/lib/permaporter

[ "$(id -u)" -eq 0 ] || { echo "请用 sudo 运行"; exit 1; }

echo "==> 安装二进制"
install -m 0755 "${SRC_DIR}/permaporter" "$BIN"

echo "==> 创建用户与数据目录"
id -u permaporter &>/dev/null || useradd --system --home "$DATA_DIR" --shell /usr/sbin/nologin permaporter
# 数据/配置目录显式建齐（服务首启要在配置目录写 permaporter.toml 模板，缺目录即拒绝启动）
install -d -m 0750 "$DATA_DIR" "$DATA_DIR/data" "$DATA_DIR/config"
# 属主统一纠正：曾以 root 手工运行过会留下 root 属主的目录与库文件
chown permaporter:permaporter "$DATA_DIR" "$DATA_DIR/data" "$DATA_DIR/config"
chown -R permaporter:permaporter "$DATA_DIR/config"
if [ -f "$DATA_DIR/data/permaporter.db" ]; then
  chown permaporter:permaporter "$DATA_DIR/data/permaporter.db"
fi

echo "==> 写入环境配置（五端口变量，已存在则跳过）"
if [ ! -f "$ENV_FILE" ]; then
  install -m 0640 "${SRC_DIR}/permaporter.env.example" "$ENV_FILE"
  echo "    已生成 $ENV_FILE（默认 UI_PORT=9090 NPM_PORT=9091 GO_PORT=9092 PY_PORT=9093 MVN_PORT=9094）"
else
  echo "    $ENV_FILE 已存在，保持原值"
fi

echo "==> 安装 systemd 单元"
install -m 0644 "${SRC_DIR}/permaporter.service" "/etc/systemd/system/${SERVICE_NAME}.service"
systemctl daemon-reload

echo "==> 启动服务"
systemctl enable --now "$SERVICE_NAME"

echo
echo "安装完成。自检："
echo "  curl http://127.0.0.1:9090/healthz   # 控制台聚合（含 go/npm/py/mvn 四段）"
echo "  curl http://127.0.0.1:9091/healthz   # npm"
echo "  curl http://127.0.0.1:9092/healthz   # GOPROXY"
echo "  curl http://127.0.0.1:9093/healthz   # PyPI Simple"
echo "  curl http://127.0.0.1:9094/healthz   # Maven 仓库"
echo "客户端配置："
echo "  go env -w GOPROXY=http://<本机地址>:9092,direct"
echo "  npm config set registry http://<本机地址>:9091/"
echo "首次初始化：浏览器打开 http://<本机地址>:9090/（数据库未配置时自动进入部署向导）"
