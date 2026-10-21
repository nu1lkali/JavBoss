#!/bin/bash
# JavBoss 镜像构建脚本（在 Linux / Kali 上运行，产物直接在群晖导入）
#
# 背景：群晖 NAS 性能弱、内存小，直接在上面编译前端 + Go 容易 OOM 或耗时很久。
#      在 Linux 机器上构建好镜像，导出 tar 拷到 NAS，docker load 即可运行。
#
# 用法：
#   bash build-image.sh                 # 构建并导出 tar
#   bash build-image.sh --mirror        # 使用国内镜像加速拉取基础镜像 + apk 源（推荐国内网络）
#   bash build-image.sh --no-export     # 只构建镜像，不导出
#   bash build-image.sh --export-only   # 只导出已构建的镜像
#
# 产物：dist/javboss-<tag>-linux-amd64.tar
#
# 前置：已安装 docker 并把当前用户加入 docker 组（或使用 sudo）。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGE_TAG="${IMAGE_TAG:-strm-local}"
TARGET_ARCH="${TARGET_ARCH:-amd64}"
USE_MIRROR=0
DO_BUILD=1
DO_EXPORT=1

info() { printf '\033[32m[JavBoss]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[JavBoss]\033[0m %s\n' "$*"; }
die()  { printf '\033[31m[JavBoss]\033[0m %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
  case "$1" in
    --mirror)      USE_MIRROR=1; shift ;;
    --no-export)   DO_EXPORT=0; shift ;;
    --export-only) DO_BUILD=0; shift ;;
    --tag)         IMAGE_TAG="$2"; shift 2 ;;
    -h|--help)     sed -n '2,17p' "$0"; exit 0 ;;
    *) die "未知参数：$1（用 --help 查看用法）" ;;
  esac
done

# ---------- 环境检查 ----------
command -v docker >/dev/null 2>&1 || die "找不到 docker。请先安装：sudo apt install -y docker.io"
if ! docker info >/dev/null 2>&1; then
  if [ "$(id -u)" -eq 0 ]; then
    die "Docker 守护进程未运行：sudo systemctl start docker"
  else
    warn "当前用户无权访问 Docker。可执行：sudo usermod -aG docker \$USER 后重新登录，"
    warn "或直接以 root 运行本脚本：sudo bash build-image.sh"
    die "中止：无 Docker 权限。"
  fi
fi

# ---------- 架构确认 ----------
HOST_ARCH="$(uname -m)"
case "$HOST_ARCH" in
  x86_64)  [ "$TARGET_ARCH" = "amd64" ] || warn "宿主是 x86_64，将为 $TARGET_ARCH 构建（需 binfmt/QEMU，速度较慢）" ;;
  aarch64) [ "$TARGET_ARCH" = "arm64" ] || warn "宿主是 aarch64，将为 $TARGET_ARCH 构建（需 binfmt/QEMU，速度较慢）" ;;
  *) warn "未识别的宿主架构：$HOST_ARCH" ;;
esac
info "宿主架构 $HOST_ARCH → 目标 linux/${TARGET_ARCH}"

# ---------- 构建参数 ----------
BUILD_ARGS=(
  --build-arg BUILD_MODE=release
  --build-arg "APP_VERSION=${IMAGE_TAG}"
  --build-arg NPM_CONFIG_REGISTRY=https://registry.npmmirror.com
  --build-arg "GOPROXY=https://goproxy.cn,direct"
  --build-arg "APK_MIRROR=${APK_MIRROR:-mirrors.aliyun.com}"
  -t "javboss:${IMAGE_TAG}"
  -f Dockerfile
)

if [ "$USE_MIRROR" -eq 1 ]; then
  # 通过镜像加速站拉取 Docker Hub 的基础镜像，避免 auth.docker.io 超时。
  MIRROR="${MIRROR:-docker.m.daocloud.io/library}"
  info "使用镜像加速：${MIRROR}"
  BUILD_ARGS+=(
    --build-arg "NODE_IMAGE=${MIRROR}/node:24-bookworm-slim"
    --build-arg "GO_IMAGE=${MIRROR}/golang:1.25-bookworm"
    --build-arg "ALPINE_IMAGE=${MIRROR}/alpine:3.23"
    --build-arg "RUNTIME_IMAGE=${MIRROR}/alpine:3.23"
  )
fi

# 运行时镜像默认 alpine:3.23（ffmpeg 用 apk 装，不走 GitHub），可用环境变量覆盖。
if [ -n "${RUNTIME_IMAGE:-}" ]; then
  info "运行时镜像：${RUNTIME_IMAGE}"
  BUILD_ARGS+=(--build-arg "RUNTIME_IMAGE=${RUNTIME_IMAGE}")
fi

cd "$SCRIPT_DIR"
mkdir -p dist

# ---------- 构建 ----------
if [ "$DO_BUILD" -eq 1 ]; then
  NATIVE_ARCH="amd64"
  [ "$HOST_ARCH" = "aarch64" ] && NATIVE_ARCH="arm64"

  if [ "$TARGET_ARCH" = "$NATIVE_ARCH" ]; then
    # 目标架构与宿主一致：用经典构建最稳，不需要 buildx / QEMU。
    info "开始构建 javboss:${IMAGE_TAG}（linux/${TARGET_ARCH}，首次约 5-15 分钟）..."
    BUILD_CMD=(docker build "${BUILD_ARGS[@]}" .)
  else
    warn "跨架构构建：宿主 ${NATIVE_ARCH} → 目标 ${TARGET_ARCH}，需要 binfmt/QEMU，速度会慢很多。"
    docker buildx version >/dev/null 2>&1 || die "跨架构构建需要 buildx。请安装 docker-buildx 或改在目标架构机器上构建。"
    BUILD_CMD=(docker buildx build --platform "linux/${TARGET_ARCH}" --load "${BUILD_ARGS[@]}" .)
  fi

  if ! "${BUILD_CMD[@]}"; then
    warn "构建失败。常见原因："
    warn "  1. 拉取基础镜像失败 —— 加上 --mirror 参数重试，例如："
    warn "       sudo bash build-image.sh --mirror"
    warn "  2. 拉取 alpine 基础镜像失败 —— 换国内镜像：RUNTIME_IMAGE=docker.m.daocloud.io/library/alpine:3.23 sudo -E bash build-image.sh"
    warn "  3. ffmpeg 装不上（apk 源不通）—— 换 APK 镜像：APK_MIRROR=mirrors.tuna.tsinghua.edu.cn sudo -E bash build-image.sh"
    warn "  4. 磁盘空间不足 —— 构建过程需要约 5GB 空闲空间，检查：df -h /var/lib/docker"
    warn "  5. 无法访问 registry.npmmirror.com / goproxy.cn —— 检查 DNS 与代理设置。"
    die "中止：镜像构建失败。"
  fi
  info "镜像构建完成：javboss:${IMAGE_TAG}"
fi

# ---------- 导出 ----------
if [ "$DO_EXPORT" -eq 1 ]; then
  docker image inspect "javboss:${IMAGE_TAG}" >/dev/null 2>&1 || die "本地没有 javboss:${IMAGE_TAG}，无法导出。"
  OUT="dist/javboss-${IMAGE_TAG}-linux-${TARGET_ARCH}.tar"
  info "导出镜像到 ${OUT}（约 150-250MB）..."
  docker save -o "$OUT" "javboss:${IMAGE_TAG}"
  info "导出完成：$(pwd)/${OUT}"
  echo
  echo "=============================================="
  echo " 下一步：把 tar 传到群晖并导入"
  echo "=============================================="
  echo " 1) 传输（在 Kali 上执行，把 IP 换成 NAS 地址）："
  echo "      scp $(pwd)/${OUT} admin@<NAS-IP>:/volume1/docker/"
  echo
  echo " 2) 登录 NAS："
  echo "      ssh admin@<NAS-IP>"
  echo
  echo " 3) 导入镜像："
  echo "      sudo docker load -i /volume1/docker/$(basename "$OUT")"
  echo
  echo " 4) 启动（项目目录已在 NAS 上时）："
  echo "      cd /volume1/docker/JavBoss"
  echo "      sudo bash deploy-synology.sh --no-build"
  echo
  echo " 注意：deploy-synology.sh 会写入 APP_VERSION=${IMAGE_TAG}，"
  echo "       与导入的镜像 tag 一致，可直接启动。"
fi
