# PermaPorter 单镜像五端口：9090 控制台 / 9091 npm Registry / 9092 GOPROXY / 9093 PyPI Simple / 9094 Maven 仓库。
# 前端（Vite + TailwindCSS + pnpm）与后端（Go）分别构建，前端产物经 go:embed
# 嵌入单一二进制。最终镜像基于官方 alpine，补装 nginx 做对外反代。
FROM node:22-alpine AS web-build

WORKDIR /web

COPY frontend/package.json frontend/pnpm-lock.yaml ./
RUN corepack enable && pnpm install --frozen-lockfile

COPY frontend/ ./
# 产物输出到 ../internal/core/webui/assets（含 .vite/manifest.json，见 vite.config.js）
RUN pnpm build

FROM golang:1.27.1-alpine AS api-build

WORKDIR /src

# 版本号由 Makefile 统一传入，禁止在镜像内自动生成。
ARG VERSION
RUN test -n "$VERSION" || (echo "VERSION build arg is required; build via make docker-build" >&2; exit 1)

RUN go env -w GO111MODULE=on
RUN go env -w GOPROXY=https://proxy.golang.org,direct

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/

# 前端产物不入库，由 web-build 阶段生成后放入 embed 目录
COPY --from=web-build /internal/core/webui/assets ./internal/core/webui/assets

# Alpine 镜像默认不含时区数据库，装好后需复制到最终镜像才能让 TZ=Asia/Shanghai 生效
RUN apk add --no-cache tzdata

# 纯 Go 构建：不依赖 CGO（SQLite 驱动为 modernc.org/sqlite），
# 因此不需要 gcc/musl-dev，产物是完全静态的单二进制。
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
  go build -trimpath \
    -ldflags="-s -w -X 'permaporter/internal/config.Version=${VERSION}'" \
    -o /out/permaporter ./cmd/permaporter

FROM alpine:3.20 AS final

# 官方 alpine 不含 nginx，这里补装（对外统一反代五端口）
RUN apk add --no-cache nginx

WORKDIR /app

COPY --from=api-build /out/permaporter /app/permaporter
# 时区数据来自构建阶段（alpine 基础镜像默认不带 tzdata）
COPY --from=api-build /usr/share/zoneinfo /usr/share/zoneinfo

RUN rm -rf /etc/nginx/http.d/default.conf
COPY build/nginx/nginx.conf /etc/nginx/nginx.conf

RUN mkdir -p /app/data /app/config /run/nginx

# 对外由 nginx 统一暴露五端口；后端进程只监听本机回环 8090/8091/8092/8093/8094。
# 后端端口经环境变量注入：首次启动自动生成的 permaporter.toml 模板会预填这些
# 生效值（文件 > 环境变量 > 默认值），容器行为与配置文件层引入前保持一致。
ENV GIN_MODE=release
ENV UI_PORT=8090
ENV NPM_PORT=8091
ENV GO_PORT=8092
ENV PY_PORT=8093
ENV MVN_PORT=8094
# 服务器配置文件目录（permaporter.toml：数据库与端口；模板由应用启动流程自动生成）
ENV CONFIG_DIR=/app/config
EXPOSE 9090 9091 9092 9093 9094

# 数据目录挂载点：单库 permaporter.db（平台凭据 + 各生态数据，go_*/npm_*/py_*/mvn_* 前缀表）+ 四棵存储树
VOLUME ["/app/data"]
# 配置目录挂载点：permaporter.toml（首次启动生成模板，初始化向导写回；postgresql 密码明文存储于其中）
VOLUME ["/app/config"]

ENTRYPOINT ["/bin/sh", "-c", "nginx && exec /app/permaporter"]
