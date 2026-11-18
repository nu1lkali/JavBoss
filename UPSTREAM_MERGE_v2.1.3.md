# 上游合并说明 · JavBoss v2.1.3

> 合并时间：2026-10-26
> 上游仓库：`https://github.com/Solr159/JavBoss`（remote `upstream`）
> 本地仓库：`D:\project\JavBoss`（remote `origin` → `nu1lkali/JavBoss`）

---

## 一、结论速览

| 项目 | 内容 |
| --- | --- |
| 上游合并点 | `0eabaf0` — `chore: update release version to v2.1.3 (#414)`（tag `v2.1.3`） |
| 合并前本地 main | `085765b` — `支持Emby联动` |
| 合并后 main | `cacb8cd` — `Merge upstream Solr159/JavBoss v2.1.3 into main` |
| 上游新增提交 | 4 个（2026-10-24 ~ 2026-10-25） |
| 冲突数量 | **0 处**（7 个双方都改过的文件全部自动合并） |
| 变更规模 | 52 个文件，+3113 / −1458 |
| 本地特性 | 全部保留，未被覆盖 |
| 本地额外补丁 | `614f56f` — 恢复目录扫描清理的三道保护（有意偏离上游） |
| 追加本地提交 | `560c910` — 统一 LF 换行、修复 README 编码损坏、新增 `.gitattributes`（见 §九） |

提交图：

```
*   cacb8cd Merge upstream Solr159/JavBoss v2.1.3 into main
|\
| * 0eabaf0 chore: update release version to v2.1.3 (#414)
| * ac22068 Simplify release-draft workflow (#413)
| * a6c4972 Optimize UX (#412)
| * 7dfb0fd feat: support portrait mode (#411)
* | 2a9a6f9 支持Emby联动
* | cd10f34 Update GlobalSettings.jsx
* | b9bbe38 兼容strm、元数据回写视频目录、联动emby
|/
```

---

## 二、上游这 4 个提交都改了什么

| 提交 | 日期 | 标题 | 一句话说明 |
| --- | --- | --- | --- |
| `7dfb0fd` | 2026-10-24 | feat: support portrait mode (#411) | 新增「竖版卡片」布局，配套封面裁剪位置字段、裁剪弹窗、数据库迁移 |
| `a6c4972` | 2026-10-24 | Optimize UX (#412) | 显示设置界面整体重构（JavSettings → DisplaySettings），新增视频显示子面板 |
| `ac22068` | 2026-10-25 | Simplify release-draft workflow (#413) | 发布草稿流水线简化：输入参数、资产命名、tag 冲突检查 |
| `0eabaf0` | 2026-10-25 | chore: update release version to v2.1.3 (#414) | 版本号升到 v2.1.3：README 下载链接、安装脚本 |

---

## 三、功能级改动明细

### 1. 竖版（Portrait）卡片模式 — #411

**数据库 / 模型**

- `jav` 表新增列 `cover_crop_position`，类型 `real NOT NULL DEFAULT 1`（0=左，1=右，控制封面在竖版卡片里的水平裁剪位置）。
- 新迁移：`internal/db/migrations/202610240001_add_jav_cover_crop_position.go`，使用 `addColumnIfMissing`，**不可回滚**（`irreversibleMigration`）。启动时自动执行。
- `internal/models/jav.go`：`Jav` 结构体新增 `CoverCropPosition float64`（`json:"cover_crop_position"`，默认 1），其余字段仅做 gofmt 对齐。

**后端逻辑**

- `internal/db/jav.go`
  - `JavUpdateInput` 新增 `CoverCropPosition *float64`，写入前校验 `0 ≤ x ≤ 1`，并拒绝 `NaN` / `Inf`。
  - 刮削写库 `saveJavInfoTx` 把 `cover_crop_position` 加入 `omit` 列表 —— **重新刮削不会覆盖用户手动选的裁剪位置**。
  - 新建 JAV 记录时默认 `CoverCropPosition: 1`。
- `internal/api/jav_api.go`：`PATCH /jav/:id` 请求体新增 `cover_crop_position` 字段并透传。
- `internal/api/config_api.go`：新增配置项 `jav_card_portrait`（布尔），持久化到应用配置。

**前端**

| 文件 | 变化 |
| --- | --- |
| `web/src/features/jav/components/JavCard.jsx` | 重写（约 +363/−297），支持竖/横两种呈现 |
| `web/src/features/jav/components/JavCoverCropModal.jsx` | **新增**（153 行），封面裁剪弹窗 |
| `web/src/features/jav/coverCrop.js` | **新增**，裁剪位置与百分比换算 |
| `web/src/index.css` | 新增竖版卡片相关样式 |
| `JavGrid / JavCardTags / JavMetadataIcons / WatchTimeIcons / JavIdolCoverModal` | 适配竖版尺寸与信息密度 |
| `useJavItemActions / useJavPresentation` | 增加裁剪入口与展示字段 |

**测试**：`jav_cover_crop_api_test.go`、`jav_cover_crop_test.go`、`coverCrop.test.js`、`JavCard.browser.test.js`（+314 行）。

> ✅ **这一处已按 fork 语义恢复（提交 `614f56f`）**
> 上游在 #411 里顺手删掉了 `deleteUnprocessedVideoLocations` 的三道保护，本 fork **已原样恢复**：
> 1. 扫描有读错误（`summary.ReadErrors > 0`）→ 整次清理跳过；
> 2. 删除前对每个 stale location 做 `os.Lstat` 复查，文件仍存在就保留；
> 3. 删除前 `os.Stat` 复查扫描根目录，离线 / 不是目录则报错退出。
>
> 恢复后的语义：**只有「完整扫描 + 文件确实不存在」才会删 `video_location`**；删除仍然只删位置映射，video 元数据与观看记录保留。
>
> 代价（需要知道）：这是本地有意偏离上游的一处补丁。`internal/directory/directory_scan_test.go` 已同步改成 fork 语义 —— 上游新增的 `TestScanDirectoryCleanupAfterScanOutcome` 中「probe failure（文件还在也照样清理）」场景与新行为直接冲突，已移除该场景；`testScanDirectoryWithUnreadableSubdirectory` 的期望值改回「部分扫描保留全部 location（Removed=0、count=3）」。以后再次合并上游时这两个文件会冲突，按「保留本地保护」处理即可。

---

### 2. 显示设置界面重构 — #412

| 变化 | 说明 |
| --- | --- |
| `JavSettings.jsx` → `DisplaySettings.jsx` | 重命名 + 改写（相似度 63%） |
| `JavSettingsModal.jsx` → `DisplaySettingsModal.jsx` | 大幅重写（约 711 行变动），按 tab 拆分 |
| `VideoDisplaySettings.jsx` | **新增**（152 行），视频显示专项子面板 |
| `VideoSettings.jsx`、`VideoSettingsModal.jsx` | **删除** |
| `settings/actions.js` | 重构，`saveJavSettings(draft, onWaterfallChange, tab)` 增加 tab 参数 |
| `settings/model.js` | 配置模型重排 |
| `config_api.go` | 新增配置项 `jav_hide_studio`（隐藏片商，与已有 `jav_hide_series / jav_hide_idols / jav_hide_tags` 对齐） |
| `App.jsx`、`SideTabs.jsx`、`useSectionNavigation.js`、`DirectoryManager.jsx` | 导航与目录管理的交互微调 |

**测试**：新增 `DisplaySettings.browser.test.js`（344 行）、`settings.test.js`（127 行）。

> 兼容性：仓库内已无对 `JavSettingsModal` / `VideoSettings*` 的引用（已 grep 全量确认），`App.jsx` 改为 `import DisplaySettings`。如果你有自定义前端或浏览器扩展引用了旧组件名，需要同步改名。

---

### 3. release-draft 流水线简化 — #413

`.github/workflows/release-draft.yml`（+14/−12）：

- 手动触发输入由 `tag` + `version` 两项合并为一项 `release`（例如 `v1.8.2`）。
- 全局 `env.RELEASE = ${{ inputs.release }}`，构建、资产命名、release 标题全部统一用 `$RELEASE`。
- 发布 job 增加 `actions/checkout@v5`，改用 `git ls-remote --tags origin refs/tags/$RELEASE` 判断 tag 是否已存在（原来是 `gh release view`）。

> 影响：**手动发版时填写方式变了**，只填一个版本号字段。

---

### 4. 版本号 → v2.1.3 — #414

- `README.md`：四个平台的下载链接 v2.1.2 → v2.1.3。
- `scripts/install.sh` / `scripts/install.ps1`：默认安装版本 → v2.1.3。

---

## 四、你本地的改动全部保留

下列本地独有改动（3 个本地提交 + 未提交内容）与上游改动没有冲突，合并后仍在：

**本地提交（b9bbe38 / cd10f34 / 085765b 的内容）**

- Emby 联动：`internal/emby/*`
- strm 兼容：`internal/util/strm.go`、`strm_test.go`
- 元数据回写视频目录 / 目录自动处理：`internal/directory/auto_process.go`、`nfo_rebuild.go`、`runtime.go`
- 运行时配置：`internal/common/runtimeconfig.go`
- 相关 API 与测试：`internal/api/config_api.go`、`directory_api.go`、`video_api.go`、`internal/db/store.go` 等
- 部署：`Dockerfile`、`build-image.sh`、`compose.local.yaml`、`deploy-synology.sh`、`deploy/synology/*`
- 其他：`.gitignore`、`DEVELOPMENT.md`、`scripts/cli/cli.mjs`、`web/src/features/directories/api.js`、`GlobalSettings.jsx`、`GlobalSettingsModal.jsx`

**未提交内容（保持原样）**

- `README.md` 的「赞赏支持」段落（合并后已重新追加）
- 未跟踪文件 `assets/branding/sponsor-qrcode.png`

**双方都改过、自动合并的文件（7 个）**

`internal/api/config_api.go`、`internal/api/config_api_test.go`、`internal/db/jav.go`、`internal/directory/directory_processor.go`、`directory_processor_test.go`、`internal/directory/directory_scan.go`、`web/src/index.css` —— git 三方合并后两边的逻辑都在，无人工介入。

---

## 五、合并过程记录（含一次事故，务必知悉）

1. 上游 `fetch` 到 `upstream/main` 后开始合并；
2. **合并过程中 git 进程被中断**（命令层的提前终止），导致：
   - 本地 `.git` 的对象包（`*.pack`）、`refs/`、`logs/` 被破坏；
   - 工作区 `web/src`、`web/tests` 在合并检出阶段被清空。
3. 恢复手段：
   - 对象库从 `\\192.168.2.124\docker\JavBoss\.git`（10/21 的完整副本，停在 `cd10f34`）恢复；
   - 工作区 `web/` 从同一份共享副本恢复；
   - 本地提交 `085765b 支持Emby联动` 的内容以新提交 `2a9a6f9` **重新提交**（内容一致，哈希不同）；
   - 上游对象通过 GitHub 的 smart-HTTP（`info/refs` + `git-upload-pack`）手工取回 191 KB 增量包，导入后得到**真实的上游提交**，因此合并提交保留了完整的上游父链。
4. 合并改在临时目录 `%TEMP%\jbmerge` 里完成（避开工作区的写入问题），再整体回灌到 `D:\project\JavBoss`。

**备份目录（确认无误后可自行删除）**

| 目录 | 说明 |
| --- | --- |
| `D:\project\JavBoss_git_broken_20261026` | 事故后残缺的 `.git`（对象包已丢失，仅留证据） |
| `D:\project\JavBoss_git_backup_20261026` | 事故后 `.git` 的第一次拷贝（同上，残缺） |
| `D:\project\JavBoss_git_premerge_104036` | 从共享副本恢复的完整 `.git`（停在 `cd10f34`，可作为二次兜底） |

---

## 六、验证情况

**已验证**

- 合并提交图正确，上游 4 个提交为第二父链，历史完整。
- 全量扫描无冲突标记（`<<<<<<<`）。
- 上游新增文件就位：`JavCoverCropModal.jsx`、`coverCrop.js`、`DisplaySettings.jsx`、`VideoDisplaySettings.jsx`、`202610240001_add_jav_cover_crop_position.go`。
- 上游删除文件已移除：`JavSettingsModal.jsx`、`VideoSettings.jsx`、`VideoSettingsModal.jsx`、`JavSettings.jsx`。
- 旧组件名在 `web/src` 中无残留引用，`App.jsx` 已指向 `DisplaySettings`，`DisplaySettingsModal` → `VideoDisplaySettings` 链路完整。
- 本地特性文件全部存在（`internal/emby`、`internal/util/strm.go`、`auto_process.go`、`nfo_rebuild.go`、`runtimeconfig.go`）。
- `git status` 已干净（跟踪文件 0 改动）；README 赞赏段落与 `assets/branding/sponsor-qrcode.png` 已在 `560c910` 提交，工作树不再有未提交内容（详见 §九）。
- 恢复的三道保护是**逐行还原**：已用 `git diff` 确认，`internal/directory/directory_scan.go` 与合并前 `2a9a6f9` 版本相比仅剩该函数注释差异，函数体完全一致（即 fork 上跑通过的原代码）。

**未验证**

- **没有编译，也没有跑测试**。本机没有 Go 工具链（`go` 不存在），`web/node_modules` 也不存在（需要联网 `npm install`）。
- 受影响但**未执行**的用例：`internal/directory/directory_scan_test.go` 的 `TestDeleteUnprocessedVideoLocations`、`TestScanDirectoryCleanupAfterScanOutcome`、`TestScanDirectoryContinuesAfterUnreadableSubdirectory`。这三处已按恢复后的语义改写，逻辑上与合并前 fork 的绿测一致，但请在本地或 Docker 里跑一遍确认。
- 建议至少跑一次：`go build ./...`、`go test ./internal/directory/`、`cd web && npm ci && npm run build`，或在 Docker 里走一遍 `build-image.sh`。

---

## 七、升级注意事项

1. **数据库迁移**：启动时自动执行 `202610240001_add_jav_cover_crop_position`（`ALTER TABLE jav ADD COLUMN cover_crop_position real NOT NULL DEFAULT 1`）。不可回滚，旧库平滑加列。
2. **新配置项**：`jav_card_portrait`（竖版卡片）、`jav_hide_studio`（隐藏片商）——前端默认关，可在显示设置里开。
3. **旧前端引用**：任何外部代码引用 `JavSettings` / `JavSettingsModal` / `VideoSettings` / `VideoSettingsModal` 都要改为 `DisplaySettings` / `DisplaySettingsModal` / `VideoDisplaySettings`。
4. **CI 发版**：`release-draft` 工作流的输入参数从 `tag`+`version` 变为单个 `release`。
5. **目录扫描删除逻辑**：上游删了保护，本 fork 已恢复（见 §3.1）。以后重新合上游时，务必确认这道保护没被合没。
6. **推送**：本地历史与 `origin/main` 已分叉（本地有合并提交），推之前确认是否需要 `git push origin main`；`085765b` 与 `2a9a6f9` 内容相同但哈希不同，远端保留的仍是 `085765b`。

---

## 八、完整变更清单（52 个文件）

### 后端（Go）

> 注：`internal/directory/directory_scan.go` 与 `directory_scan_test.go` 的上游改动已在本地提交 `614f56f` 中回滚为 fork 语义，见 §3.1。

| 状态 | 文件 |
| --- | --- |
| M | `internal/api/config_api.go` |
| M | `internal/api/config_api_test.go` |
| M | `internal/api/jav_api.go` |
| A | `internal/api/jav_cover_crop_api_test.go` |
| M | `internal/db/jav.go` |
| A | `internal/db/jav_cover_crop_test.go` |
| M | `internal/db/jav_schema_test.go` |
| A | `internal/db/migrations/202610240001_add_jav_cover_crop_position.go` |
| A | `internal/db/migrations/202610240001_add_jav_cover_crop_position_test.go` |
| M | `internal/directory/directory_processor.go` |
| M | `internal/directory/directory_processor_test.go` |
| M | `internal/directory/directory_scan.go` |
| M | `internal/directory/directory_scan_test.go` |
| M | `internal/models/jav.go` |

### 前端（web/src）

| 状态 | 文件 |
| --- | --- |
| M | `web/src/App.jsx` |
| M | `web/src/app/layout/SideTabs.jsx` |
| M | `web/src/features/directories/components/DirectoryManager.jsx` |
| M | `web/src/features/jav/components/JavCard.jsx` |
| M | `web/src/features/jav/components/JavCardTags.jsx` |
| A | `web/src/features/jav/components/JavCoverCropModal.jsx` |
| M | `web/src/features/jav/components/JavGrid.jsx` |
| M | `web/src/features/jav/components/JavIdolCoverModal.jsx` |
| M | `web/src/features/jav/components/JavMetadataIcons.jsx` |
| A | `web/src/features/jav/coverCrop.js` |
| M | `web/src/features/jav/hooks/useJavItemActions.js` |
| M | `web/src/features/jav/hooks/useJavPresentation.js` |
| M | `web/src/features/playback/components/WatchTimeIcons.jsx` |
| M | `web/src/features/settings/actions.js` |
| R | `JavSettings.jsx` → `DisplaySettings.jsx` |
| A | `web/src/features/settings/components/DisplaySettingsModal.jsx` |
| D | `web/src/features/settings/components/JavSettingsModal.jsx` |
| M | `web/src/features/settings/components/TitleTranslationBatchSettings.jsx` |
| A | `web/src/features/settings/components/VideoDisplaySettings.jsx` |
| D | `web/src/features/settings/components/VideoSettings.jsx` |
| D | `web/src/features/settings/components/VideoSettingsModal.jsx` |
| M | `web/src/features/settings/model.js` |
| M | `web/src/index.css` |
| M | `web/src/navigation/useSectionNavigation.js` |

### 前端测试（web/tests）

`app.browser.test.js`、`features/jav/components/JavCard.browser.test.js`、`features/jav/components/JavEditModal.browser.test.js`、`features/jav/coverCrop.test.js`(A)、`features/playback/components/WatchTimeIcons.browser.test.js`、`features/settings/DisplaySettings.browser.test.js`(A)、`features/settings/TitleTranslationSettings.browser.test.js`、`features/settings/settings.test.js`、`fixtures/app.jsx`、`fixtures/titleTranslationSettings.jsx`

### CI / 脚本 / 文档

| 状态 | 文件 |
| --- | --- |
| M | `.github/workflows/release-draft.yml` |
| M | `README.md` |
| M | `scripts/install.ps1` |
| M | `scripts/install.sh` |

---

## 九、CRLF 事故与修复（`560c910`）

这一节解释你在群晖上遇到的那个报错，属于合并的**收尾问题**，不是上游代码问题。

### 症状

```
nu1l@nu1lkali:/volume1/docker/JavBoss$ sudo bash deploy-synology.sh --upgrade
deploy-synology.sh: line 16: $'\r': command not found
: invalid option nameine 17: set: pipefail
```

### 根因

Windows 侧 `core.autocrlf=true`（系统级配置）在检出/写盘时把所有文本文件写成了 **CRLF**。bash 逐行解析时把行尾的 `\r` 当成命令的一部分：

- `set -euo pipefail\r` → `set` 收到非法选项名 `pipefail\r`，报 `invalid option name`；
- 行号错位后，空行变成 `\r`，于是报 `$'\r': command not found`。

`deploy-synology.sh` 第 17 行正是 `set -euo pipefail`，所以这个脚本必然第一个报错——**不是脚本写错了**。

### 修复内容

| 动作 | 说明 |
| --- | --- |
| 工作树全量转 LF | 756 个文本文件按字节把 `CRLF → LF`，`deploy-synology.sh` / `build-image.sh` / `Dockerfile` / `scripts/*.sh` 全部 `CR=0` |
| 本地 git 配置 | `core.autocrlf=false`、`core.eol=lf`（只作用于本仓库，不动系统全局设置） |
| 重建索引 | `git read-tree HEAD`，清掉 758 条"假修改"（内容已一致但 stat 缓存仍是旧的）——重建后跟踪文件 0 改动 |
| 新增 `.gitattributes` | `* text=auto eol=lf`；`*.sh`/`Dockerfile`/`*.yaml` 显式 LF；`*.bat|cmd|ps1|rc` 保留 CRLF；图片等标 `binary`。**这是防复发的关键**，以后不管系统 autocrlf 怎么设都以它为准则 |
| 修复 README 编码损坏 | 之前追加的「赞赏支持」段落被 GBK/UTF-8 双重编码成乱码（`## 璧炶祻鏀寔`），且 `alt="微信赞赏码"` 的引号被吞成 `?`。已按正确 UTF-8 重写，HTML 属性补全 |
| 纳入赞赏码图片 | `assets/branding/sponsor-qrcode.png` 之前是未跟踪文件，README 引用它会 404，现已入库 |

### 共享目录同步结果（`\\192.168.2.124\docker\JavBoss`）

- 源码树：759 个文件覆盖，**未使用 `/MIR`**（避免误删 `rebuild.sh`、`deploy/synology/.env` 等 NAS 本地文件），只做增量覆盖；
- `.git` 目录：精确镜像，共享侧 `HEAD` 与本地一致，`git status` 跟踪文件 0 改动；
- 行尾复核：共享侧全部 `*.sh` 的 `CR=0`，`Dockerfile`、`*.yaml` 同样 `CR=0`；
- 上游删除的 3 个旧组件（`JavSettingsModal.jsx`、`VideoSettings.jsx`、`VideoSettingsModal.jsx`）在共享侧确认不存在。

### 现在在群晖上怎么跑

```bash
cd /volume1/docker/JavBoss
sudo bash deploy-synology.sh --upgrade
```

正常应当看到这两行（第 194 行的日志是专门用来确认跑的是新脚本）：

```
[JavBoss] 构建方式：docker build 经典构建器（DOCKER_BUILDKIT=0，不使用 compose/BuildKit）
[JavBoss] 开始构建镜像（首次需要编译前端与后端，约 5-15 分钟，取决于 NAS 性能）...
```

如果仍然报 `$'\r'`，说明看到的是旧文件（SMB 缓存或复制未落盘），用 `file deploy-synology.sh` 或 `cat -A deploy-synology.sh | head -3` 确认行尾没有 `^M`。
