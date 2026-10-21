# syntax=docker/dockerfile:1.7
#
# 基础镜像支持通过构建参数覆盖，便于在国内网络下拉取（例如换成镜像加速地址）：
#   docker build \
#     --build-arg NODE_IMAGE=docker.m.daocloud.io/library/node:24-bookworm-slim \
#     --build-arg GO_IMAGE=docker.m.daocloud.io/library/golang:1.25-bookworm \
#     ...
# 不传则使用官方源。
#
# 说明：本文件刻意不使用 RUN --mount=type=cache，以兼容群晖等关闭 BuildKit 的场景
# （群晖上 BuildKit 会出现 "failed to read dockerfile" 的挂载点问题）。
# 代价是重复构建时无法复用 npm/go 模块缓存。
#
# 注意：所有 ARG *_IMAGE 必须声明在第一个 FROM 之前（全局作用域），
# 否则 legacy builder 在后续 FROM 中会报 "base name should not be blank"。

ARG NODE_IMAGE=node:24-bookworm-slim
ARG GO_IMAGE=golang:1.25-bookworm
# 运行时必须与 go-build 使用同一套 C 运行库。
# go-sqlite3 是 cgo 驱动，golang:1.25-bookworm 产出的是 glibc 动态链接二进制，
# 放进 alpine（musl）会报 "exec ./javboss: no such file or directory"。
ARG RUNTIME_IMAGE=debian:bookworm-slim

FROM ${NODE_IMAGE} AS web-build

ARG NPM_CONFIG_REGISTRY=https://registry.npmmirror.com
ENV NPM_CONFIG_REGISTRY=${NPM_CONFIG_REGISTRY}

WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci --prefer-offline
COPY web/ ./
RUN npm run build

FROM ${GO_IMAGE} AS go-build

ARG GOPROXY=https://goproxy.cn,direct
ARG GOSUMDB=sum.golang.org
ARG BUILD_MODE=release
ARG APP_VERSION=dev
# sqlite 需要 cgo；显式打开并设置好 CGO_ENABLED/CC，
# 避免不同 golang 基础镜像默认值不一致导致 "conn.Exec undefined" 之类的怪错误。
ARG CGO_ENABLED=1
ENV GOPROXY=${GOPROXY} \
  GOSUMDB=${GOSUMDB} \
  CGO_ENABLED=${CGO_ENABLED} \
  CC=gcc \
  GOTOOLCHAIN=local

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 构建前先确认工具链齐全：golang:1.25-bookworm 自带 gcc，这里只做断言不安装。
RUN set -eux; \
  go version; \
  gcc --version | head -n 1; \
  go env CGO_ENABLED CC GOARCH GOOS
RUN go build -trimpath -ldflags="-s -w -X main.buildMode=${BUILD_MODE} -X javboss/internal/common.Version=${APP_VERSION}" -o /out/javboss ./cmd/server && \
  ls -lh /out/javboss

# 运行时基础镜像：debian:bookworm-slim，与 go-build 同为 glibc，cgo 产物可直接运行。
FROM ${RUNTIME_IMAGE}

# FFmpeg 用 apt 从软件源安装，不走 GitHub（GitHub Releases 在国内经常超时）。
# APT_MIRROR 可覆盖为国内镜像，例如阿里云：
#   --build-arg APT_MIRROR=mirrors.aliyun.com
ARG APT_MIRROR=deb.debian.org
RUN set -eux; \
  if [ "${APT_MIRROR}" != "deb.debian.org" ]; then \
    sed -i "s|deb.debian.org|${APT_MIRROR}|g; s|security.debian.org|${APT_MIRROR}|g" \
      /etc/apt/sources.list.d/debian.sources /etc/apt/sources.list 2>/dev/null || true; \
  fi; \
  apt-get update; \
  DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
    ca-certificates \
    tzdata \
    ffmpeg; \
  rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=go-build /out/javboss ./javboss
COPY --from=web-build /src/web/dist ./web/dist

# 让程序优先使用镜像内已安装的 ffmpeg/ffprobe，
# 避免它再去下载一份到 data 目录。
# 容器模式下程序只认 /app/internal/bin/{ffmpeg,ffprobe}，
# 这里把软件包自带的二进制软链进去（软链可以，硬链接不行：
# 跨层硬链接会破坏镜像层复用）。
# 注意：debian:bookworm-slim 不含 file 命令，不要在这里调用它（会 exit 127 中断构建）。
# ldd 的输出若出现 "not found" 说明 glibc 与 go-build 阶段不匹配，必须让构建失败。
RUN set -eux; \
  mkdir -p ./internal/bin; \
  ln -sf /usr/bin/ffmpeg ./internal/bin/ffmpeg; \
  ln -sf /usr/bin/ffprobe ./internal/bin/ffprobe; \
  ./internal/bin/ffmpeg -version | head -n 1; \
  ./internal/bin/ffprobe -version | head -n 1; \
  if ldd ./javboss 2>&1 | grep -q 'not found'; then \
    echo "ERROR: 动态链接库缺失，运行时镜像与 go-build 的 C 运行库不一致" >&2; \
    ldd ./javboss 2>&1; \
    exit 1; \
  fi

# JAVBOSS_HOST_PATH_PREFIX 默认为 0：本部署方案把宿主机目录直挂到容器内同名路径，
# 容器内路径与宿主机一致，不需要 /host 前缀换算。
# 只有"容器只挂 /host 单点"的部署形态才需要把它设为 1；
# 误设为 1 会让前端把每个路径都拼上 /host，导致目录浏览全部返回"目录不存在"。
ENV JAVBOSS_CONTAINER=1 \
  JAVBOSS_HOST_PATH_PREFIX=0 \
  JAVBOSS_PROXY_HOST_GATEWAY=1 \
  JAVBOSS_POSTER_CROP_LEFT=0.53 \
  JAVBOSS_POSTER_FANART=1 \
  JAVBOSS_AUTO_PROCESS=sidecar \
  JAVBOSS_AUTO_PROCESS_COVER_WAIT=5m

EXPOSE 17654
VOLUME ["/app/data"]

CMD ["./javboss"]
