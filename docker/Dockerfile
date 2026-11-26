# Lemi 镜像：前端构建 → Go 构建（纯 Go、关闭 CGO，前端嵌进可执行文件）→ 预装 mysqldump → debian 运行镜像（非 root）。
#
# 构建上下文是仓库根目录：
#   docker build -f docker/Dockerfile -t lemi .
# 运行（更完整的配置见 docker/docker-compose.yml）：
#   docker run -d --name lemi -p 7700:7700 -v lemi-data:/data ghcr.io/lemonitcn/lemi:latest
#   docker logs lemi            # 首次启动打印一次性初始化令牌
#
# 数据目录 /data 就是全部数据（元数据库、主密钥、上传的 SQLite、本地备份），务必挂卷并备份。
# 运行镜像用 debian:bookworm-slim 而不是 distroless/alpine：官方 mysqldump / mysql 客户端是 glibc 程序。
# 官方镜像源慢时可以换，例如：
#   --build-arg NODE_IMAGE=docker.1panel.live/library/node:22-bookworm-slim
#   --build-arg GO_IMAGE=docker.1panel.live/library/golang:1.26-bookworm
#   --build-arg RUNTIME_IMAGE=docker.1panel.live/library/debian:bookworm-slim
#   --build-arg APT_MIRROR=mirrors.aliyun.com --build-arg GOPROXY=https://goproxy.cn,direct
#   --build-arg NPM_REGISTRY=https://registry.npmmirror.com/

ARG NODE_IMAGE=node:22-bookworm-slim
ARG GO_IMAGE=golang:1.26-bookworm
ARG RUNTIME_IMAGE=debian:bookworm-slim

# ---------- 前端 ----------
FROM --platform=$BUILDPLATFORM ${NODE_IMAGE} AS console
ARG NPM_REGISTRY=https://registry.npmjs.org/
WORKDIR /src/lemi-console
# lemi-console 还没有提交时也能构建（只是程序里只有占位页）：COPY 里带一个必定存在的文件，可选文件用通配
COPY LICENSE lemi-consol[e]/package*.json ./
RUN --mount=type=cache,target=/root/.npm \
    if [ -f package.json ]; then \
      npm config set registry "$NPM_REGISTRY" && npm config set replace-registry-host always && \
      npm ci --no-audit --no-fund; \
    fi
COPY LICENSE lemi-consol[e] ./
RUN if [ -f package.json ]; then npm run build; else mkdir -p dist; fi

# ---------- 服务 ----------
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=
ARG COMMIT=
ARG BUILD_DATE=
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=$GOPROXY GOTOOLCHAIN=local CGO_ENABLED=0
WORKDIR /src/lemi-service
COPY lemi-service/go.mod lemi-service/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY lemi-service/ ./
COPY --from=console /src/lemi-console/dist/ ./internal/web/dist/
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    set -eu; \
    M=github.com/LemonITCN/lemi/lemi-service/internal/version; \
    LDF="-s -w"; \
    if [ -n "$VERSION" ]; then LDF="$LDF -X $M.Version=$VERSION"; fi; \
    if [ -n "$COMMIT" ]; then LDF="$LDF -X $M.Commit=$COMMIT"; fi; \
    if [ -n "$BUILD_DATE" ]; then LDF="$LDF -X $M.Date=$BUILD_DATE"; fi; \
    GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags "$LDF" -o /out/lemi ./cmd/lemi

# ---------- mysqldump / mysql 客户端（按清单下载并校验，清单不存在时跳过）----------
FROM --platform=$BUILDPLATFORM ${RUNTIME_IMAGE} AS tools
ARG TARGETARCH
ARG APT_MIRROR=
ARG TOOLS_BASE_URL=https://github.com/LemonITCN/lemi/releases/download
RUN set -eux; \
    if [ -n "$APT_MIRROR" ]; then sed -i "s|deb.debian.org|$APT_MIRROR|g" /etc/apt/sources.list.d/debian.sources; fi; \
    apt-get update; \
    apt-get install -y --no-install-recommends ca-certificates curl jq; \
    rm -rf /var/lib/apt/lists/*
COPY lemi-service/go.mod lemi-service/internal/dumptool[s]/manifest.json /tmp/tools/
COPY docker/fetch-mysql-tools.sh /usr/local/bin/fetch-mysql-tools.sh
RUN sh /usr/local/bin/fetch-mysql-tools.sh /tmp/tools/manifest.json "linux-$TARGETARCH" /opt/lemi/tools "$TOOLS_BASE_URL"

# ---------- 运行 ----------
FROM ${RUNTIME_IMAGE}
ARG APT_MIRROR=
RUN set -eux; \
    if [ -n "$APT_MIRROR" ]; then sed -i "s|deb.debian.org|$APT_MIRROR|g" /etc/apt/sources.list.d/debian.sources; fi; \
    apt-get update; \
    # libncurses6：官方 mysql 客户端（还原 mysqldump 备份时用）依赖它，不随客户端包打包
    apt-get install -y --no-install-recommends ca-certificates tzdata libncurses6; \
    rm -rf /var/lib/apt/lists/*; \
    groupadd --system --gid 10001 lemi; \
    useradd --system --uid 10001 --gid 10001 --home-dir /data --no-create-home --shell /usr/sbin/nologin lemi; \
    mkdir -p /data; \
    chown 10001:10001 /data; \
    chmod 700 /data
COPY --from=tools /opt/lemi/tools /opt/lemi/tools
COPY --from=build /out/lemi /usr/local/bin/lemi
# LEMI_TOOLS_PREINSTALLED：镜像里预装的 mysqldump 位置（<目录>/mysql-<版本>/linux-<架构>/），/data/tools 会被数据卷覆盖所以放这里
ENV LEMI_IN_DOCKER=1 \
    LEMI_DATA_DIR=/data \
    LEMI_LISTEN=0.0.0.0:7700 \
    LEMI_TOOLS_PREINSTALLED=/opt/lemi/tools \
    HOME=/data
USER 10001:10001
WORKDIR /data
VOLUME ["/data"]
EXPOSE 7700
HEALTHCHECK --interval=30s --timeout=10s --start-period=60s --retries=3 CMD ["/usr/local/bin/lemi", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/lemi"]
CMD ["serve"]
