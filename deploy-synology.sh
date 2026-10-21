#!/bin/bash
# JavBoss 群晖 NAS 一键部署脚本（含 STRM 支持）
#
# 用法：
#   bash deploy-synology.sh                     # 交互式部署
#   bash deploy-synology.sh --media /volume1/video --port 17654
#   bash deploy-synology.sh --upgrade           # 只重建镜像并重启，保留数据
#   bash deploy-synology.sh --no-build          # 用已导入的镜像启动，不在 NAS 上编译
#   bash deploy-synology.sh --status            # 查看运行状态
#
# 设计原则：
#   * 不需要手动装 Go / Node / GCC，编译全部在 Docker 里完成；
#     也可以先在电脑上用 build-image.sh 导出镜像，这里加 --no-build 直接启动；
#   * 所有可变配置写入 deploy/synology/.env，脚本可重复执行；
#   * 不会删除已有数据目录，升级不会丢库。

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$SCRIPT_DIR/deploy/synology"
ENV_FILE="$DEPLOY_DIR/.env"
COMPOSE_FILE="$DEPLOY_DIR/docker-compose.yaml"

# ---------- 输出 ----------
info() { printf '\033[32m[JavBoss]\033[0m %s\n' "$*"; }
warn() { printf '\033[33m[JavBoss]\033[0m %s\n' "$*"; }
die()  { printf '\033[31m[JavBoss]\033[0m %s\n' "$*" >&2; exit 1; }

# ---------- 参数 ----------
MEDIA_DIR=""
MEDIA_DIR_IN_CONTAINER=""
PORT=""
UPGRADE_ONLY=0
STATUS_ONLY=0
SKIP_BUILD=0

while [ $# -gt 0 ]; do
  case "$1" in
    --media)      MEDIA_DIR="$2"; shift 2 ;;
    --media-in-container) MEDIA_DIR_IN_CONTAINER="$2"; shift 2 ;;
    --port)       PORT="$2"; shift 2 ;;
    --upgrade)    UPGRADE_ONLY=1; shift ;;
    --status)     STATUS_ONLY=1; shift ;;
    --no-build)   SKIP_BUILD=1; shift ;;
    -h|--help)    sed -n '2,14p' "$0"; exit 0 ;;
    *) die "未知参数：$1（用 --help 查看用法）" ;;
  esac
done

# ---------- 环境自检 ----------
require_cmd() {
  command -v "$1" >/dev/null 2>&1 || die "缺少命令：$1。请先在 DSM 套件中心安装「Container Manager」。"
}

detect_compose() {
  if docker compose version >/dev/null 2>&1; then
    COMPOSE="docker compose"
  elif command -v docker-compose >/dev/null 2>&1; then
    COMPOSE="docker-compose"
  else
    die "未找到 docker compose。DSM 7 请安装 Container Manager；DSM 6 请先安装 docker-compose。"
  fi
}

require_cmd docker
docker info >/dev/null 2>&1 || die "无法连接 Docker 守护进程。请确认已用 root 或加入 docker 组执行，并在 Container Manager 中启动 Docker。"
detect_compose
[ -f "$COMPOSE_FILE" ] || die "找不到编排文件：$COMPOSE_FILE"

if [ "$STATUS_ONLY" -eq 1 ]; then
  ( cd "$DEPLOY_DIR" && $COMPOSE --env-file "$ENV_FILE" ps )
  exit 0
fi

# ---------- 配置采集 ----------
# 复用已有 .env，保证脚本可重复运行。
strip_quotes() {
  local v="${1:-}"
  v="${v#\"}"; v="${v%\"}"
  v="${v#\'}"; v="${v%\'}"
  printf '%s' "$v"
}
if [ -f "$ENV_FILE" ]; then
  # shellcheck disable=SC1090
  . "$ENV_FILE"
  # .env 里的值按 docker compose 规范可能带引号，shell 读入后需去掉。
  MEDIA_DIR="$(strip_quotes "${MEDIA_DIR:-}")"
  MEDIA_DIR_IN_CONTAINER="$(strip_quotes "${MEDIA_DIR_IN_CONTAINER:-}")"
  DATA_DIR="$(strip_quotes "${DATA_DIR:-}")"
  APP_VERSION="$(strip_quotes "${APP_VERSION:-}")"
  TZ="$(strip_quotes "${TZ:-}")"
  info "已加载现有配置：$ENV_FILE"
fi

ask() {
  local prompt="$1" default="$2" answer
  if [ -n "$default" ]; then
    printf '%s [%s]: ' "$prompt" "$default" >/dev/tty
  else
    printf '%s: ' "$prompt" >/dev/tty
  fi
  read -r answer </dev/tty || answer=""
  [ -z "$answer" ] && answer="$default"
  printf '%s' "$answer"
}

if [ -z "${MEDIA_DIR:-}" ]; then
  if [ "$UPGRADE_ONLY" -eq 0 ]; then
    MEDIA_DIR="$(ask '媒体根目录（存放 STRM 与视频）' "${MEDIA_DIR:-/volume1/video}")"
  else
    MEDIA_DIR="${MEDIA_DIR:-/volume1/video}"
  fi
fi
[ -d "$MEDIA_DIR" ] || die "媒体目录不存在：$MEDIA_DIR。请先在 File Station 创建，或改用 --media 指定。"

# 容器内路径默认与宿主机一致：STRM 文件里写的本地路径才能原样命中。
MEDIA_DIR_IN_CONTAINER="${MEDIA_DIR_IN_CONTAINER:-$MEDIA_DIR}"

if [ -z "${JAVBOSS_PORT:-}" ]; then
  JAVBOSS_PORT="${PORT:-17654}"
elif [ -n "$PORT" ]; then
  JAVBOSS_PORT="$PORT"
fi

# 数据目录默认与仓库目录（/volume1/docker/JavBoss）平级，命名为 javboss-data：
# 一眼能看出是数据目录，也不会再和仓库目录只差大小写（群晖 ext4 大小写敏感，
# javboss 与 JavBoss 能同时存在，旧默认 /volume1/docker/javboss/data 就因此被误删过）。
DATA_DIR="${DATA_DIR:-/volume1/docker/javboss-data}"
# 老 .env 仍指向旧默认路径且该目录已不存在时，自动改用新默认并写回 .env，
# 避免把数据又写回一个已被删除/混淆的路径；目录还在则维持原值，不动老部署。
if [ "$DATA_DIR" = "/volume1/docker/javboss/data" ] && [ ! -d "$DATA_DIR" ]; then
  warn "旧默认数据目录 $DATA_DIR 不存在，改用新默认 /volume1/docker/javboss-data"
  DATA_DIR="/volume1/docker/javboss-data"
fi
APP_VERSION="${APP_VERSION:-strm-local}"
TZ="${TZ:-Asia/Shanghai}"

# 群晖上目录属主通常是 admin(1026)、用户自身 UID 或 root，取媒体目录的属主最稳妥。
PUID="${PUID:-$(stat -c '%u' "$MEDIA_DIR" 2>/dev/null || echo 1000)}"
PGID="${PGID:-$(stat -c '%g' "$MEDIA_DIR" 2>/dev/null || echo 1000)}"

mkdir -p "$DATA_DIR" 2>/dev/null || true
if [ ! -w "$DATA_DIR" ]; then
  warn "数据目录不可写：$DATA_DIR"
  warn "请执行：sudo chown -R ${PUID}:${PGID} \"$DATA_DIR\"   然后重新运行本脚本。"
  die "中止：数据目录权限不足。"
fi

cat > "$ENV_FILE" <<EOF
# 由 deploy-synology.sh 生成，可手工修改后重复执行脚本。
# 含逗号或空格的取值必须加引号，否则 docker compose 解析 .env 时会截断。
MEDIA_DIR="$MEDIA_DIR"
MEDIA_DIR_IN_CONTAINER="$MEDIA_DIR_IN_CONTAINER"
DATA_DIR="$DATA_DIR"
JAVBOSS_PORT=$JAVBOSS_PORT
PUID=$PUID
PGID=$PGID
TZ=$TZ
APP_VERSION=$APP_VERSION
NPM_CONFIG_REGISTRY="https://registry.npmmirror.com"
GOPROXY="https://goproxy.cn,direct"
GOSUMDB="sum.golang.org"
JAVBOSS_PROXY_HOST_GATEWAY=0
EOF
info "配置已写入：$ENV_FILE"

# ---------- 构建与启动 ----------
cd "$DEPLOY_DIR"

if [ "$SKIP_BUILD" -eq 0 ]; then
  ARCH="$(uname -m)"
  case "$ARCH" in
    x86_64|amd64) ;;
    aarch64|arm64)
      warn "检测到 ARM 架构（$ARCH）。Dockerfile 已支持 arm64 构建，但需要 NAS 内存 ≥ 2GB，"
      warn "且基础镜像 golang:1.25-bookworm 需要在 NAS 上运行，编译会明显更慢（可能 20-40 分钟）。" ;;
    *) warn "未验证的架构：$ARCH。构建可能失败。" ;;
  esac
  # 编译期最少需要约 1.5GB 内存；群晖默认的 Docker 内存限制偏小。
  MEM_MB="$(awk '/MemAvailable/ {print int($2/1024)}' /proc/meminfo 2>/dev/null || echo 0)"
  if [ "${MEM_MB:-0}" -gt 0 ] && [ "$MEM_MB" -lt 1200 ]; then
    warn "可用内存仅 ${MEM_MB}MB，前端构建（Vite）很可能 OOM 被强制中断。"
    warn "建议先关闭其他容器，或在 Dockerfile 中改用已经构建好的 web/dist。"
  fi

  info "开始构建镜像（首次需要编译前端与后端，约 5-15 分钟，取决于 NAS 性能）..."
  info "本机架构：$ARCH"

  # 不用 docker compose build：群晖的 BuildKit 在读取构建上下文里的 Dockerfile 时
  # 会把它当成空文件传输（日志里是 "transferring dockerfile: 2B"），随后报
  # failed to read dockerfile: open .../buildkit-mount*/Dockerfile: no such file or directory。
  # 直接调用经典构建器 docker build 可以绕开这条路（与手动构建命令一致）。
  # 这行日志专门用来确认跑的是新脚本：看不到它就说明 NAS 上的脚本还是旧版本。
  info "构建方式：docker build 经典构建器（DOCKER_BUILDKIT=0，不使用 compose/BuildKit）"
  export DOCKER_BUILDKIT=0
  export COMPOSE_DOCKER_CLI_BUILD=0
  BUILD_ARGS=(
    --build-arg BUILD_MODE=release
    --build-arg "APP_VERSION=${APP_VERSION}"
    --build-arg "NPM_CONFIG_REGISTRY=${NPM_CONFIG_REGISTRY:-https://registry.npmmirror.com}"
    --build-arg "GOPROXY=${GOPROXY:-https://goproxy.cn,direct}"
    --build-arg "GOSUMDB=${GOSUMDB:-sum.golang.org}"
  )
  if [ -n "${RUNTIME_IMAGE:-}" ]; then
    info "运行时基础镜像：${RUNTIME_IMAGE}"
    BUILD_ARGS+=(--build-arg "RUNTIME_IMAGE=${RUNTIME_IMAGE}")
  fi
  BUILD_ARGS+=(
    -t "javboss:${APP_VERSION}"
    -f "$SCRIPT_DIR/Dockerfile"
    "$SCRIPT_DIR"
  )
  if ! docker build "${BUILD_ARGS[@]}"; then
    warn "构建失败。常见原因："
    warn "  1. 拉取基础镜像超时 —— Container Manager「注册表」里配置镜像加速，或给 Docker 配置代理；"
    warn "  2. 内存不足被 OOM 杀掉 —— 前端构建需要约 1.5GB 内存；"
    warn "  3. 无法访问 npm / goproxy —— 脚本已默认用国内源，仍失败请检查 NAS 的 DNS。"
    warn "  —— 备选方案：在电脑上运行 build-image.sh 导出镜像 tar，"
    warn "     在 NAS 上 docker load 后，再运行 bash deploy-synology.sh --no-build 启动。"
    die "中止：镜像构建失败。"
  fi
  info "镜像构建完成：javboss:${APP_VERSION}"
else
  # --no-build：镜像应从外部导入（见 build-image.sh），这里只校验它确实存在。
  if ! docker image inspect "javboss:${APP_VERSION}" >/dev/null 2>&1; then
    warn "本地没有镜像 javboss:${APP_VERSION}，--no-build 无法启动。"
    warn "请先在电脑上运行 build-image.sh 导出 tar，再在 NAS 执行："
    warn "  docker load -i javboss-${APP_VERSION}-linux-<arch>.tar"
    warn "若想直接在 NAS 上编译，去掉 --no-build 重新运行本脚本。"
    die "中止：缺少本地镜像。"
  fi
  info "使用本地已有镜像 javboss:${APP_VERSION}（跳过构建）"
fi

info "启动容器 ..."
$COMPOSE --env-file "$ENV_FILE" up -d --remove-orphans

info "等待服务就绪 ..."
READY=0
for _ in $(seq 1 45); do
  # /api/videos 是最轻量的只读接口，能返回 200 即说明路由与数据库都已就绪。
  if curl -fsS "http://127.0.0.1:${JAVBOSS_PORT}/api/videos?limit=1" >/dev/null 2>&1; then
    READY=1
    break
  fi
  sleep 2
done

IP="$(ip route get 1 2>/dev/null | awk '{print $7; exit}' || true)"
[ -z "${IP:-}" ] && IP="<NAS-IP>"

echo
if [ "$READY" -eq 1 ]; then
  info "部署完成。"
else
  warn "容器已启动，但 60 秒内未通过 HTTP 探测，请查看日志："
  warn "  cd \"$DEPLOY_DIR\" && $COMPOSE logs --tail=100 javboss"
fi
echo "  访问地址 : http://${IP}:${JAVBOSS_PORT}"
echo "  媒体目录 : ${MEDIA_DIR}  ->  容器内 ${MEDIA_DIR_IN_CONTAINER}（可写，需要生成 NFO/封面）"
echo "  数据目录 : ${DATA_DIR}"
echo
info "下一步：在网页「设置 → 目录」中添加媒体目录，路径填容器内路径 ${MEDIA_DIR_IN_CONTAINER}"
info "STRM 支持已内置：.strm 文件会被识别为媒体条目，按文件名刮削番号并生成同名 NFO 与封面。"
