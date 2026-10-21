# 自动整理设计说明（JavBoss / 群晖 STRM 库）

配套开关见 `deploy/synology/README.md` 第六节。本文只讲为什么这么设计、哪些坑要避开。

## 一、原来的问题

扫描（`runDirectoryScanWithSession`）只做两件事：把文件写入 `video_location`，再把能解析出番号的条目刮削进 `jav` 表。
NFO 与封面是 `ProcessDirectory` 写的，而它只能由用户点「整理」触发。

也就是说，刮削完成后库里已经有全部元数据，但磁盘上什么都没有，Emby 只能显示一张从别处来的图。

## 二、目标

视频入库后，无需人工干预，自动补齐：

```
xxx.strm
xxx.nfo                 <- 元数据
xxx-poster.jpg          <- 竖版海报（从原封面的右半部分裁出）
xxx-fanart.jpg          <- 完整封面（母版）
```

## 三、三个必须避开的天坑

### 3.1 不能在扫描线程里等封面

`writeJavPoster` 在封面缺失时会走 `ensureCover`，那是**同步**下载，带 30 秒超时（`coverEnsureTimeout`）。
整库首次入库时队列里有几千条，逐条等 30 秒是灾难性的。

第一版实现把「等封面 + 整理」直接写在扫描 goroutine 里，结果是：
扫描状态已经显示结束，但整理要等 `JAVBOSS_AUTO_PROCESS_COVER_WAIT`（当时是 30 分钟）才启动，
用户看到的现象就是"扫描完了什么也没发生"。

现在 `queueAutoProcess` **只入队就返回**，扫描线程立刻结束；等待与执行都在独立的后台 worker 里。

### 3.2 整理本身也不能等封面

即使队列已经"排空"判断过，仍可能有条目拿不到图（下载失败、源站没有）。
手动整理时单条等 30 秒无所谓，自动轮次里就是长尾。

所以自动整理走 `withSkipCoverWait(ctx)`：`writeJavPoster` 遇到本地没有封面时，
只调用 `cover.Enqueue` 登记，记 `posterPending` 后立刻返回，不等下载。

手动点「整理」不受影响，仍是原来的同步等待行为。

### 3.3 只看 NFO 会永远补不上封面

`writeJavSidecars` 的执行顺序是**先写 NFO，再写封面**。
封面拿不到时 `writeJavPoster` 返回 `("", nil)`，NFO 已经落盘了。

如果"是否已整理"只看 NFO，这些缺图条目会被判成已完成，再也不会被整理触及。

现在 `javSidecarComplete(videoPath, requirePoster)` 要求 **NFO 归 JavBoss 所有** 且 **海报文件非空**。
未启用封面下载（没有 `CoverDir`）时退化为只看 NFO，否则队列永远清不空。

## 四、封面队列怎么判断"排空"

`cover.PendingCount()` 返回 `queue.scheduled` 的长度。

`Enqueue` 是**先写 `scheduled` 再往 channel 里塞**，worker 处理完在 `defer` 里 `clearScheduled`，
所以这个长度同时涵盖"排队中"和"正在下载"，为 0 就是真的没有活干了。

## 五、开关：环境变量管"模式"，设置页管"开关"

两者分工明确，缺一不可：

| 来源 | 键 | 作用 | 改动后 |
| --- | --- | --- | --- |
| 容器环境变量 | `JAVBOSS_AUTO_PROCESS` | 用哪种方式整理（`sidecar` / `organize` / `organize_with_sidecar` / `none`） | 需重建容器 |
| 配置表 | `jav_auto_process_enabled` | 现在要不要跑（设置页「扫描后自动整理」） | 立即生效 |

合并规则在 `resolveAutoProcessMode(envMode, enabled, configured)`：

- 环境变量不是白名单里的模式 → 关闭。网页上的开关此时**不可用**（`jav_auto_process_available=false`）
  且强制显示为关，避免出现"开关打开了但什么都不发生"。
- 配置表里没有这个键（老库升级、从没点过开关）→ 跟随环境变量，升级后行为不变。
- 显式 `false` → 关闭。这一条必须存在，否则"关掉"会被默认值覆盖回去。

开关是每次执行时从数据库读的，不是启动时缓存：

- 入队前检查：关闭时 `queueAutoProcess` 直接返回，连磁盘都不探。
- worker 每轮（30 秒）检查：关闭就 `clear()` 掉整个队列，日志 `auto process disabled, dropped N queued job(s)`。
- **已经开始的那一轮整理无法中途取消**，会自然跑完；要立刻停只能停容器。

打开开关时，`updateConfig` 会异步调用 `directory.QueueAutoProcessForAll(ctx)`，
把所有可用目录重新评估一遍，因此不用等下一轮扫描就能补齐存量。

## 六、调度模型

```
扫描结束
  └─ queueAutoProcess(dir)              非阻塞：探测 pending，入队，立即返回
        │
        ├─ 环境变量未配置模式 / 设置页开关关闭 / 目录已删除或不可用 / pending==0  -> 直接返回
        │
后台 worker（每 30 秒一轮）
  └─ runAutoProcessCycle
        ├─ 目录正在处理中（含整理后的收尾重扫）          -> 跳过
        ├─ 上一轮已启动                                 -> settle：重新统计 pending
        │     ├─ pending==0                            -> finished，出队
        │     ├─ pending 下降                           -> progress，等 5 分钟后下一轮
        │     └─ 无进展且封面还在下                     -> 等 5 分钟后下一轮
        ├─ 首轮：covers>0 且未超过 COVER_WAIT           -> 继续等（日志只打变化）
        └─ 否则                                        -> 启动一轮整理
```

两个时间参数：

| 参数 | 默认 | 作用 |
| --- | --- | --- |
| `JAVBOSS_AUTO_PROCESS_COVER_WAIT` | `5m` | **首轮**最多等封面多久，超时先写一轮 |
| `autoProcessRoundInterval`（内部常量） | `5m` | 后续轮次最小间隔，避免持续全量重扫磁盘 |

轮次上限 `autoProcessMaxAttempts = 12`。超过仍补不齐就出队，等下一次扫描重新触发——
这样"某几个文件权限不对"不会变成无限循环。

## 六、幂等：怎样才算"已整理"

| 磁盘状态 | 判定 | 整理时的行为 |
| --- | --- | --- |
| 没有 NFO | 未整理 | 写 NFO + 图片 |
| 有 NFO，但不是 JavBoss 写的 | 未整理 | **保留**别人的 NFO，只补写缺失的图片 |
| 有 JavBoss 的 NFO，没有海报 | 未整理 | 只补海报 |
| 有 JavBoss 的 NFO 且有海报 | 已整理 | 跳过 |

这里刻意把"第三方 NFO"也算未整理，因为 MetaTube 之类的工具只写 NFO 不写海报，
那种条目恰恰最需要补图；而 `writeJavSidecars` 内部通过 `errNFOUnownedByOtherTool` 保证不覆盖。

## 八、模块边界

| 文件 | 职责 |
| --- | --- |
| `internal/directory/auto_process.go` | 待整理队列、后台 worker、未完成条目探测 |
| `internal/directory/directory_processor.go` | 真正的写文件逻辑（原有）+ `skipCoverWait` 分支 + `makeJellyfinNFO` |
| `internal/directory/nfo_rebuild.go` | 空壳 NFO 的分类与批量重建（见第十一节） |
| `internal/jav/scrape/cover/queue.go` | `pendingCount()`，只读 `scheduled` 长度 |
| `internal/db/jav.go` | `ListJavProcessingCandidates`，与整理用的 SQL 同过滤条件 |
| `internal/common/runtimeconfig.go` | 环境变量解析（模式名归一化、时长解析） |

`queueAutoProcess` 只做调度，不碰文件；文件读写全部复用 `ProcessDirectory`，避免出现第二套命名规则。

## 八、与扫描会话的关系

`StartDirectoryProcessing` 内部会 `CancelAndReserveDirectoryScan`，要求调用方**已经释放**扫描会话。

三个接入点都遵守：

- 添加目录 / 修改路径：`ScanDirectory` 返回后才 `QueueAutoProcess`
- 手动扫描：goroutine 里显式 `finish()` 之后再调用
- 自动扫描：`ScanDirectory` 返回即意味着会话已释放；若返回 `ErrDirectoryScanInProgress`
  （上一轮整理后的重扫还没结束）也仍然登记一次，让它有机会补漏

整理结束后 `StartDirectoryProcessing` 会重新扫描一次目录，这属于它自己的收尾，
不会再调用 `queueAutoProcess`，因此不存在递归。

## 十、失败模式

| 情况 | 处理 |
| --- | --- |
| 封面下载全部失败 | 条目记 `posterPending`，NFO 照写；轮次用尽后出队，下次扫描重来 |
| 媒体目录只读 | 自动整理只记日志不抛错，不影响扫描与刮削 |
| 用户同时手动整理 | `ErrDirectoryWorkInProgress`，自动流程让位，等它结束后重新评估 |
| 目录被删除或路径不可用 | 探测阶段就跳过，不进入队列 |
| 设置页关掉开关 | 30 秒内清空队列；进行中的那一轮会跑完 |
| 服务重启 | 队列在内存里，丢失后由下一次扫描或下一轮自动扫描重新登记 |

## 十一、空壳 NFO 的批量重建

媒体库里经常混着别的刮削器写下的 NFO，其中最麻烦的是**空壳**：只有番号、`<plot />` 为空、
没有 genre / actor，Emby 里显示成一个光秃秃的标题。这类文件不带 `<generator>JavBoss</generator>`，
自动整理按"别人的文件"保护它们，于是永远修不掉。

设置页「目录管理 → 扫描后自动整理」下方提供了两个按钮：

- **先统计一下**（`POST /api/nfo/rebuild` + `dry_run=true`）：只统计不写盘。
- **重建空壳 NFO**（`dry_run=false`）：只重写下面三类文件。

| NFO 现状 | 分类 | 是否重写 |
| --- | --- | --- |
| 文件不存在 | `NFOMissing` | 重写（建新文件） |
| 含 `<generator>JavBoss</generator>` | `NFOOwnedByJavBoss` | 重写（刷新成最新数据） |
| 外部工具写的，plot / summary / outline / genre / actor 全空 | `NFOShell` | 重写 |
| 外部工具写的，有任一正文内容 | `NFORich` | **保留** |
| XML 解析失败 | `NFORich` | **保留**（读不懂就不碰） |

另外，数据库里没有可用元数据的记录（还没刮削成功，只有番号）会被计入 `no_metadata` 且不重写 ——
写出来还是空壳，没有意义，应该先去补刮削。

任务模型：`POST /api/nfo/rebuild` 只负责启动，立即返回 `202` 和初始状态；整库重建可能持续几分钟，
所以前端每 2 秒轮询 `GET /api/nfo/rebuild`，直到 `running` 变为 `false`，再读 `summary`。
同一时间只允许一个重建任务，重复提交会得到 `409`。

实现：`internal/directory/nfo_rebuild.go`，只调用 `writeNFOFile` + `makeJellyfinNFO`，
与自动整理共用同一套 NFO 生成逻辑，不会产生第二种格式。海报完全不动。

> JavBoss 的数据模型里没有剧情简介字段（`models.Jav` 只有 title / zh_title / studio / series /
> release / duration / tags / idols），所以重建出来的 NFO 会有 title、originaltitle、sorttitle、
> year、premiered、releasedate、runtime、tagline、outline（配信開始日）、mpaa、studio、set、
> genre、tag（有码/无码）、actor、dateadded，但 `<plot>` 依然是空的。
> 想要 plot 必须在刮削层新增字段并落库，属于另一件事。


## 十二、Emby 联动（写完 NFO 自动通知刷新）

### 为什么需要

Emby 界面永远读它自己的 `library.db`，磁盘上的 NFO 只在「扫描 / 刷新元数据」时才被重新解析。
JavBoss 重建 NFO 后，如果不通知 Emby，用户在 Emby 里看到的还是旧元数据，必须手动去点一次
「刷新元数据」，还要记得勾「替换所有元数据」。这一节就是把最后这一步自动化。

### 一个前置条件（重要）

如果 Emby 媒体库的「元数据下载器」里勾选了 MetaTube 之类的插件，Emby 展示的元数据来自
**插件在线刮削**，NFO 会被忽略 —— 这种情况下通知刷新也没有意义。请先在
「媒体库 → 编辑 → 元数据下载器」里取消勾选，让 Emby 改为读取本地 NFO。

### 配置

设置页「目录管理 → 扫描后自动整理」下方的「Emby 联动」卡片：

| 配置 | 键 | 说明 |
| --- | --- | --- |
| 开关 | `jav_emby_enabled` | 即时生效 |
| 地址 | `jav_emby_url` | 如 `http://192.168.1.10:8096`，只允许 http/https |
| API Key | `jav_emby_api_key` | Emby 控制台 → 设置 → API 密钥 → 新建 |

密钥处理与翻译配置一致：`GET /api/config` 只返回 `jav_emby_api_key_set`（是否已配置），
明文永不离开服务端，日志里也不打印；保存时传空串表示清除。

「测试连接」按钮（`POST /api/emby/test`）允许用**刚填写、还没保存**的值自检，
返回 Emby 的服务器名和版本。

### 触发时机

| 触发点 | 行为 |
| --- | --- |
| 批量重建 NFO 完成（`dry_run=false`） | 立即通知（`force=true`） |
| 自动整理完成一个目录 | 通知，但受最小间隔限制（默认 10 分钟，避免高频打扰 Emby） |

通知逻辑在 `internal/emby`：先 `GET /Library/MediaFolders` 列出媒体库，优先刷新
`CollectionType=movies` 的库，一个电影库都没有时退回刷新全部；对每个库调
`POST /Items/{id}/Refresh?Recursive=true&MetadataRefreshMode=FullRefresh&ImageRefreshMode=FullRefresh&ReplaceAllMetadata=true&ReplaceAllImages=true`。

`ReplaceAllMetadata` / `ReplaceAllImages` 必须带上，否则 Emby 默认保留条目上已有的元数据，
NFO 改了也不会生效。

未配置、配置不完整时静默跳过；失败只记日志（`emby refresh failed ...`），不影响重建和整理本身。
