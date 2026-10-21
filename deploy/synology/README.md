# 群晖 NAS 部署指南（含 STRM 支持）

本文档针对已加入 STRM 媒体支持的 JavBoss 构建，说明如何在群晖 DSM 上部署、
如何准备 STRM 文件，以及刮削出 NFO 与封面的正确用法。

---

## 一、先说清楚 STRM 是什么

`.strm` 是 Emby / Jellyfin 约定的**指针文件**：一个纯文本文件，内容是一行播放地址。
文件本身只有几百字节，不含任何视频数据。

```
miaa-131.(mkv).strm      <-  文件名承载番号，用于刮削
├─ 内容：http://192.168.1.10:5244/d/movie/miaa-131.mkv
```

支持的目标写法（与 Emby 一致）：

| 写法 | 示例 | 本项目行为 |
| --- | --- | --- |
| HTTP(S) 流 | `http://nas:5244/d/miaa-131.mkv` | 播放时 302 跳转，由播放器直连 |
| 本地路径 | `/volume1/video/miaa-131.mkv` | 代理跳转到本机流接口，可 HLS 转码 |
| Windows UNC | `\\NAS\video\miaa-131.mkv` | 同上（路径需在容器内可见） |
| Windows 盘符 | `F:\video\miaa-131.mkv` | 同上 |

注释行（`#` 开头）和空行会被跳过，读取第一条有效行；兼容 UTF-8(BOM) 与 UTF-16。

---

## 二、本次改动做了什么

| 环节 | 改动 | 位置 |
| --- | --- | --- |
| 扫描识别 | `.strm` 被接受为媒体候选，不再被扩展名白名单挡掉 | `internal/util/video.go` |
| 元数据探测 | STRM **不交给 ffprobe**（否则必然失败），改为解析指针内容生成指纹 | `internal/directory/directory_scan.go` |
| 去重 | 指纹 = 指针目标地址，扩展名变了（换清晰度）就会重新入库刮削 | `internal/util/strm.go` |
| 番号提取 | 处理 `xxx.(mkv).strm`、`xxx.mp4.strm` 等重复/括号扩展名，避免番号被污染 | `internal/util/jav_util.go` |
| 刮削过滤 | STRM 无时长，跳过"时长 < 900 秒视为花絮"的规则 | `internal/directory/video_location_jav_linker.go` |
| 缩略图 | STRM 无法抓帧，跳过并回落使用番号封面 | `internal/video/screenshot/queue.go` |
| 播放 | `/streams` 返回真实地址；`/stream` 对 STRM 做 302 跳转 | `internal/api/video_api.go` |

**NFO 与图片命名规则**：全部与视频同名（仅替换扩展名），后缀分别是 `.nfo`、`-poster`、`-fanart`。

```
av/MIGD-641/
├── MIGD-641.(mkv).strm          # 视频指针
├── MIGD-641.(mkv).nfo           # 元数据
├── MIGD-641.(mkv)-poster.jpg    # 封面：裁掉左侧 53% 的竖版主图
└── MIGD-641.(mkv)-fanart.jpg    # 背景图：整幅封面（JAVBOSS_POSTER_FANART=0 可关闭）
```

为什么用 `{name}-poster` 而不是裸名 `poster.jpg`：Emby 的 Primary 图片优先序列是
`{name}.ext` → `{name}-poster.ext` → … → `poster.ext`，带名字的写法优先级更高；
而且它不要求"一片一目录"，一个目录放多部影片或多个清晰度版本也不会互相覆盖。
裸名 `poster.jpg` 只在视频独占一个文件夹时才有效。

> `poster` 与 `fanart` 同源于 `data/cover/<番号>.jpg`：fanart 是整幅母版，
> poster 是它裁掉左侧后的右半部分，所以调大裁剪比例随时可以从 fanart 重裁。

---

## 三、部署前准备

### 3.1 检查条件

- DSM 7.x，已安装 **Container Manager**（DSM 6 为 Docker 套件）
- NAS 可用内存 ≥ 2 GB（首次编译前端较吃内存）
- 至少留出 3 GB 空间给镜像和数据

### 3.2 在 File Station 里准备目录

建议如下结构，**媒体目录与数据目录分开**：

```
/volume1/docker/javboss/data          <- 数据库、封面缓存（可写）
/volume1/video                        <- 媒体库根目录（只读挂载）
    └── JAV/
        └── miaa-131.(mkv).strm
```

在 File Station 中对这两个目录右键 → 属性 → 权限，确认你的 DSM 账号有读写权限。

---

## 四、一键部署

把整个项目目录上传到 NAS，例如 `/volume1/docker/JavBoss`，然后 SSH 登录执行：

```bash
cd /volume1/docker/JavBoss
sudo bash deploy-synology.sh
```

脚本会依次完成：

1. **环境自检** —— 检查 docker、compose 是否可用、守护进程是否在运行
2. **采集配置** —— 交互式询问媒体目录与端口，其余（PUID/PGID/TZ）自动推断
3. **权限处理** —— 自动取媒体目录的属主 UID/GID，检查数据目录可写
4. **写配置文件** —— 生成 `deploy/synology/.env`（可重复执行）
5. **构建镜像** —— 在容器内编译 Go + 前端，NAS 上无需安装任何工具链
6. **启动并自检** —— 轮询 `/api/videos`，成功后打印访问地址

也支持非交互参数：

```bash
# 指定目录与端口
sudo bash deploy-synology.sh --media /volume1/video --port 17654

# 仅重建镜像并重启（保留数据库）
sudo bash deploy-synology.sh --upgrade

# 用已导入的镜像启动，不在 NAS 上编译
sudo bash deploy-synology.sh --no-build

# 查看容器状态
bash deploy-synology.sh --status
```

### 4.1 在电脑上构建镜像，NAS 只负责运行（推荐）

群晖的 CPU 偏弱，编译前端和 Go 后端可能要 5-40 分钟，还可能因内存不足被 OOM 杀掉。
更省事的做法是在你自己的电脑上构建，导出 tar 后导入 NAS：

**第 1 步（电脑上）** —— 确认 Docker Desktop 已启动：

```bash
bash build-image.sh                  # 默认 x86_64 架构
bash build-image.sh --arch arm64     # 群晖是 ARM 机型（如 DS223）时用
```

产物是 `dist/javboss-strm-local-linux-amd64.tar`。

**第 2 步（NAS 上）** —— 把 tar 传到 NAS（File Station 拖拽即可）：

```bash
docker load -i /volume1/docker/javboss-strm-local-linux-amd64.tar
cd /volume1/docker/JavBoss
sudo bash deploy-synology.sh --no-build
```

`--no-build` 会跳过编译，直接校验本地镜像是否存在然后启动。

> **改了源码就必须重新构建**。`--no-build` 是"用现成镜像启动"，源码改动不会被编进容器。
> 改完代码后的正确顺序是：
>
> ```bash
> # NAS 上直接编译（脚本内部已关闭 BuildKit，不需要另外设置环境变量）
> sudo bash deploy-synology.sh --upgrade
>
> # 或者：电脑上重新 build-image.sh -> scp -> docker load -> NAS 上 --no-build
> ```
>
> 想确认容器里跑的是不是新代码：
> `sudo docker compose -f deploy/synology/docker-compose.yaml logs javboss | head -n 20`，
> 看有没有 `auto process enabled: mode=sidecar` 这类新日志。

> **确认群晖架构**：SSH 执行 `uname -m`，`x86_64` 用 `amd64`，`aarch64` 用 `arm64`。
> 架构不匹配时容器会报 `exec format error`。

### 4.2 直接在 NAS 上构建的注意事项

首次构建要拉取 `node:24-bookworm-slim`、`golang:1.25-bookworm`、`alpine:3.23` 三个基础镜像，
国内网络可能较慢。在 **Container Manager → 注册表 → 设置** 中配置镜像加速器，或给 Docker 配置代理后重跑脚本。

如果构建在 `npm ci` 或 `go mod download` 处失败，检查 NAS 的 DNS 是否能解析
`registry.npmmirror.com` 与 `goproxy.cn`（脚本已默认使用国内源）。

**群晖的 BuildKit 有问题**：构建上下文里的 Dockerfile 会被当成空文件传输
（日志里是 `transferring dockerfile: 2B`），随后报
`failed to read dockerfile: open /volume1/@docker/tmp/buildkit-mount.../Dockerfile: no such file or directory`。

脚本已经处理：`deploy-synology.sh` 不使用 `docker compose build`，而是直接调用经典构建器
`docker build`（并导出 `DOCKER_BUILDKIT=0`），所以直接跑脚本即可：

```bash
sudo bash deploy-synology.sh --upgrade
```

如果需要手工构建，务必带上 `DOCKER_BUILDKIT=0`：

```bash
cd /volume1/docker/JavBoss
sudo DOCKER_BUILDKIT=0 docker build -t javboss:strm-local \
  --build-arg BUILD_MODE=release --build-arg APP_VERSION=strm-local .
```

本项目 Dockerfile 已为 legacy builder 做了适配：不使用 `RUN --mount=type=cache`，
所有 `ARG *_IMAGE` 都声明在第一个 `FROM` 之前。

**FFmpeg 来源**：运行时镜像基于 Alpine，FFmpeg 直接用 `apk` 从软件源安装，
**不从 GitHub 下载静态包**（GitHub Releases 在国内经常超时，且 `TARGETARCH` 只在 BuildKit 下自动注入，
legacy builder 会报 `TARGETARCH: parameter not set`）。

apk 源默认走阿里云；如需更换，用 `APK_MIRROR` 指定：

```bash
sudo -E docker build -t javboss:strm-local \
  --build-arg BUILD_MODE=release \
  --build-arg APP_VERSION=strm-local \
  --build-arg GOPROXY=https://goproxy.cn,direct \
  --build-arg NPM_CONFIG_REGISTRY=https://registry.npmmirror.com \
  --build-arg APK_MIRROR=mirrors.tuna.tsinghua.edu.cn \
  -f Dockerfile .
```

可用镜像站：`mirrors.aliyun.com`（默认）、`mirrors.tuna.tsinghua.edu.cn`、`mirrors.ustc.edu.cn`。
如果连基础镜像都拉不动，把 `RUNTIME_IMAGE` 也换成加速地址：

```bash
--build-arg RUNTIME_IMAGE=docker.m.daocloud.io/library/alpine:3.23
```

内存低于 2GB 的机型建议直接用 4.1 的离线镜像方式。

### 4.3 权限报错

若脚本提示 "数据目录不可写"，执行：

```bash
sudo chown -R 1026:100 /volume1/docker/javboss/data
```

`1026` 是群晖上常见的管理员 UID，可用 `id` 确认你自己的 UID。

---

## 五、部署后配置

1. 浏览器打开 `http://<NAS-IP>:17654`
2. 进入 **设置 → 目录 → 添加目录**
3. 路径填写**容器内路径**，本方案下与宿主机路径一致，例如 `/volume1/video`
4. 保存后点击「扫描」

扫描完成后，每个 STRM 条目都会：

- 从文件名解析番号（如 `miaa-131`）
- 在线刮削标题、演员、发行日期、分类
- 在 `data` 目录缓存封面

---

## 六、生成 NFO 和封面文件

默认**全自动**：扫描入库 → 刮削 → 后台补封面 → 自动写 NFO 和封面。不需要手动点「整理」。

流程如下：

1. 扫描把视频写入数据库，并对每个番号发起刮削，封面进入下载队列
2. 扫描结束立刻把目录登记到「待整理队列」——**这一步不阻塞**，界面上扫描状态会正常结束
3. 后台 worker 每 30 秒看一次队列：
   - 封面还没下完 → 继续等（首轮最多等 5 分钟）
   - 封面下完或等够时间 → 启动一轮整理，在视频旁写出
     `xxx.nfo`、`xxx-poster.jpg`、`xxx-fanart.jpg`
4. 一轮整理结束后重新统计：还缺图就留着，等下一轮（间隔 5 分钟）继续补，
   直到全部补齐或达到轮次上限

所以整库首次入库不会卡住：**先写全部 NFO，封面随下载进度分批补**。

### 6.1 开关

开关分两层，各管一件事：

| 层 | 位置 | 管什么 | 改完怎么生效 |
| --- | --- | --- | --- |
| 环境变量 | `docker-compose.yaml` | **用哪种方式**整理 | 重建容器 |
| 设置页 | 设置 → 目录 → 「扫描后自动整理」 | **现在要不要跑** | 立即生效 |

网页上的开关存在数据库配置项 `jav_auto_process_enabled` 里，关掉后
扫描和刮削照旧，只是不再自动写 NFO 与封面；30 秒内会清空待整理队列
（已经开始的那一轮会跑完）。重新打开时会立刻把已有目录评估一遍，
不用等下一次扫描。

环境变量（默认已开）：

```yaml
JAVBOSS_AUTO_PROCESS: "${JAVBOSS_AUTO_PROCESS:-sidecar}"
JAVBOSS_AUTO_PROCESS_COVER_WAIT: "${JAVBOSS_AUTO_PROCESS_COVER_WAIT:-5m}"
```

| `JAVBOSS_AUTO_PROCESS` | 行为 |
| --- | --- |
| `sidecar`（默认） | 只在原位置生成 NFO 与封面，**不移动、不改名任何文件** |
| `organize_with_sidecar` | 先移动到规范目录结构，再生成 NFO 与封面 |
| `organize` | 只移动整理，不生成 NFO 与封面 |
| `none` / 留空 | 彻底关闭：网页上的开关会变成不可用（灰掉） |

> 设成 `none` 之后，网页上的开关是灰的，这表示"服务端不允许"，
> 不是开关坏了。想恢复必须改环境变量并重建容器。

`JAVBOSS_AUTO_PROCESS_COVER_WAIT` 是**首轮**等封面下载完的上限：

- `5m`（默认）：最多等 5 分钟先写一轮，剩下的封面由后续轮次补
- `-1`：完全不等，扫描完立刻整理（NFO 马上就有，封面靠后续轮次）
- `30m`：大库首次入库想一次写全再等，可以调大，但期间界面上看不到整理进度

> 后续轮次的间隔固定为 5 分钟，不受这个变量控制，避免持续全量重扫磁盘。

### 6.2 什么时候会触发

- 添加目录后的首次扫描
- 修改目录路径后触发的重扫
- 手动点「扫描」后
- 自动扫描发现新文件后

判定标准是「NFO 和海报**都**由 JavBoss 写好」。只有 NFO 没有海报的条目仍算未完成——
因为 `writeJavSidecars` 先写 NFO 再写封面，封面没下载好时只写了 NFO，
只按 NFO 判断会让这些条目永远补不上图。

第三方工具（如 MetaTube）写的 NFO 会被保留，只补写缺失的封面图片。

### 6.3 日志里会看到什么

```bash
sudo docker compose -f deploy/synology/docker-compose.yaml logs -f javboss | grep "auto process"
```

一次完整的首次入库大致是：

```
auto process enabled: mode=sidecar cover_wait=5m0s
auto process queued   id=1 path=/volume1/av mode=sidecar pending=7037 covers=6871
auto process waiting for covers id=1 pending=7037 covers=6871 waited=0s
auto process waiting for covers id=1 pending=7037 covers=4200 waited=2m10s
auto process cover wait timed out id=1 covers=4000 waited=5m0s; proceeding with cached covers
auto process started  id=1 path=/volume1/av mode=sidecar round=1 remaining=7037
auto process progress id=1 path=/volume1/av remaining=3000 rounds=1
auto process started  id=1 path=/volume1/av mode=sidecar round=2 remaining=3000
auto process finished id=1 path=/volume1/av rounds=3
```

看到 `waiting` 就是正常在等封面，不是卡死。看到 `finished` 才代表这个目录彻底处理完。

### 6.4 仍然可以手动整理

**JAV → 选择目录 → 整理**，多用于：

- 临时改成别的模式跑一次
- 自动整理被关掉后想补一次
- 想在小目录上先验证命名

推荐先在小目录上试跑 `sidecar` 确认命名符合预期。

### 6.4 命名规则

以 `MIGD-641.(mkv).strm` 为例，生成三个文件：

```
MIGD-641.(mkv).strm          ← 原视频指针文件
MIGD-641.(mkv).nfo           ← 元数据
MIGD-641.(mkv)-poster.jpg    ← 竖版海报（从 fanart 右侧裁出）
MIGD-641.(mkv)-fanart.jpg    ← 完整封面
```

带视频名前缀的写法在 Emby 的图片优先级里排在裸 `poster.jpg` 之前，且不要求"一片一目录"。

> **写保护开关**：媒体目录默认**可写**（生成 NFO、封面和移动整理都需要写权限）。
> 若你只想浏览、绝不允许改动源文件，编辑 `deploy/synology/docker-compose.yaml`，
> 把媒体卷下的 `read_only: false` 改成 `true`，然后执行：
>
> ```bash
> sudo bash deploy-synology.sh --upgrade --no-build
> ```
>
> 设为 `true` 后，自动整理会跳过（只记日志不报错），手动「整理」会报权限错误，这是预期行为。

---

## 七、STRM 命名建议

番号必须能被识别，以下写法均可正确刮削：

```
miaa-131.(mkv).strm
MIAA-131.strm
ABP-123.mp4.strm
SNIS-974.(1080p).strm
IPX-001.(h265).strm
SSIS-001.[4k].strm
FC2-PPV-4983452.strm
```

不推荐：把番号写错或用中文全角字符代替连字符。

---

## 八、排查

```bash
# 实时日志（观察扫描与刮削）
cd /volume1/docker/JavBoss/deploy/synology
sudo docker compose logs -f javboss

# 确认 STRM 被识别
sudo docker compose logs javboss | grep "scan file"

# 进入容器确认路径可见
sudo docker compose exec javboss ls /volume1/video
```

**常见现象**

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| 扫描后看不到 STRM 条目 | 目录添加到宿主机路径但容器内未挂载该路径 | 检查 `.env` 中 `MEDIA_DIR` 与容器内路径 |
| 条目录入但没有封面 | 刮削失败或番号没解析对 | 日志中看 `link video location->jav`，并核对文件名 |
| 点击播放无反应 | STRM 内是内网地址但客户端不在同网段 | 改成 NAS 可访问的地址，或改用本地路径写法 |
| NFO 没生成 | 自动整理被关掉、封面队列未排空，或媒体卷被设为 `read_only: true` | 见第六节 |
| 缩略图一直是封面 | 正常现象，STRM 无法抓帧 | 无需处理 |

**确认自动整理在工作**

```bash
# 启动时应有一行（说明开关生效）
sudo docker compose logs javboss | grep "auto process enabled"
# auto process enabled: mode=sidecar cover_wait=5m0s

# 扫描后应能看到入队、等待与启动
sudo docker compose logs javboss | grep "auto process"
# auto process queued  id=1 path=/volume1/av mode=sidecar pending=7037 covers=6871
# auto process waiting for covers id=1 pending=7037 covers=6871 waited=0s
# auto process started id=1 path=/volume1/av mode=sidecar round=1 remaining=7037
```

只有 `queued` 没有 `started`，且伴随 `waiting for covers`，说明正在等封面下载，
这是正常等待而不是卡死；封面下完或等满 `JAVBOSS_AUTO_PROCESS_COVER_WAIT` 就会启动。

出现 `directory busy` 表示用户正在手动扫描/整理该目录，自动流程会让位，等它结束。

如果日志里连 `queued` 都没有，先看设置页 **目录 →「扫描后自动整理」** 是不是被关掉了：

```bash
# 读当前开关状态（true/false），以及是否被环境变量禁用
curl -s -b "你的会话 cookie" http://127.0.0.1:17654/config |
  grep -o '"jav_auto_process[^,]*'
```

或者在网页上直接看：开关为关、或开关变灰并提示"环境变量已关闭"，就是没生效的原因。
关掉之后，日志里还会出现 `auto process disabled, dropped N queued job(s)`。

---

## 九、升级与备份

- **备份**：只需备份 `/volume1/docker/javboss/data`（SQLite 数据库 + 封面）
- **改了源码**：`sudo bash deploy-synology.sh --upgrade`（必须重新构建，`--no-build` 不会编译新代码）
- **卸载**：`sudo docker compose down`（数据目录保留，手动删除才彻底清除）
