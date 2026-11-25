# `kkpan-cli` (`kkpan`)

夸克网盘 Go 命令行与日式二次元全屏终端客户端。

参考并复刻 [pan-tools (`pant`)](https://github.com/qwerprog/pan-tools) 的单向分层架构、终端视觉美学、自动化输出规范与高速分片传输思路，针对夸克网盘的 **FID 目录树模型**、**阿里云 OSS 上传回调链路** 与 **异步任务轮询协议** 从零重构实现。

---

## 核心特性

- **终端扫码登录与多账号管理（`kkpan login -qr` / `-cookies`）**
  - 支持直接通过手机夸克 App 扫描终端字符二维码登录（同步生成 `0600` 权限临时 `qrcode.png` 并在结束后自动清理），以及粘贴浏览器 Cookies 登录。
  - 请求拦截响应 `Set-Cookie`，自动合并并持久化刷新后的 `__puus` 会话凭证。
- **日式二次元全屏网盘浏览器（`kkpan ui` / `tui` / `browse`）**
  - 依据 [DESIGN.md](./DESIGN.md) 设计规范，提供双栏「星海档案图鉴」（左栏索引列表 + 右栏档案详览）与异步后台「传输星港」双视图（按 `Tab` 一键切换）：
    - 支持方向键 / `jkhl` 浏览、`Space` 多选、`/` 实时模糊过滤、`s` 切换排序、`n` 新建目录、`r` 重命名、`x` 回收站删除。
    - **非阻塞多任务并行队列**：按 `d` 下载或 `u` 上传立即进入后台并发队列（默认支持 3 个下载 / 2 个上传任务同时进行），顶栏实时显示传输速率与活跃数，无需等待即可自由切换其他文件夹继续挑文件；在传输中心支持筛选、`+/-` 实时调并发、`a` 一键开启「全部同时下载」、取消（`c`）、重试（`r`）及清空已完成记录（`C`）。
  - 内置三套源自 `design-pipeline` 风格的日式二次元主题，在 TUI 中按 `t` 键实时切换并自动保存：
    - `celestial`（**星穹星海** · 默认）：深空星轨鎏金 + 天青路径 + 樱绯点睛
    - `dossier`（**终末档案**）：冰川信号青 + 等宽编号档案终端
    - `shinbo`（**物语赤黑**）：朱绯红 + 和纸白高对比图鉴风格
- **夸克原生路径 ↔ FID 双向解析器**
  - 自动处理 POSIX 路径与夸克底层 `fid` 的映射及 TTL 缓存，写操作精准失效缓存；同时支持 `fid:<id>` 直连语法规避同目录重名文件歧义。
- **高速传输引擎**
  - **下载（`kkpan download` / `d`）**：多线程 HTTP Range 分片并发下载、`.kkpan.state.json` 断点续传、令牌桶限速、MD5 自动校验与 `--mtime` 同步。
  - **上传（`kkpan upload` / `u`）**：单遍流式 MD5+SHA1 校验优先触发云端**秒传**；未命中时走阿里云 OSS 分片并发上传、`.kkpan.upload.json` 断点续传与 `x-oss-callback` 合并落盘，支持 `skip` / `overwrite` / `rename` 重名策略。
  - **种子与磁力解析下载（`kkpan bt` / `torrent` / `magnet` / `offlinedl`）**：支持识别本地/网盘/远程 `.torrent` 种子、`magnet:?` 磁力链接与裸 InfoHash，支持 `--list` 预览内部文件、按 `--files` 序号或 `--match` 通配符选择性下载，并可通过 `--upload` 下载完成后自动同步到夸克网盘（支持 `--delete-local` 清理本地缓存）。
- **分享转存、分享管理与回收站**
  - **分享转存（`kkpan transfer` / `tf`）**：兼容裸链接、带 `?pwd=` 链接与整段中文分享口令文本，支持 `--list` 预览、按 `--fid` 筛选、`--collect` 自动归档建目录及 `--download` 转存即下。
  - **分享管理（`kkpan share set/list/cancel`）**：支持公开或 4 位提取码加密分享、永久 / 1 / 7 / 30 天有效期、分享统计查看与批量取消。
  - **回收站管理（`kkpan recycle list/restore/delete`）**：支持列表检索、按 `record_id` 还原、彻底删除与 `--all` 清空。
- **脚本与自动化友好**
  - 支持 `-q/--quiet`、`--no-color`（兼容 `NO_COLOR`）、`--ascii`（纯 7-bit ASCII 降级）以及 `--json` / `--plain`（无边框 TSV）结构化输出，严格分离 `stdout` 数据与 `stderr` 状态提示。

---

## 安装方式

### 1. Windows PowerShell 一键安装 (`irm`)

```powershell
irm https://raw.githubusercontent.com/A-510/kkpan-cli/main/install.ps1 | iex
```

### 2. npm / bun 安装或免安装运行

```bash
# npm 全局安装
npm i -g kkpan-cli

# bun 全局安装
bun add -g kkpan-cli

# 或免安装直接执行
bunx kkpan-cli --help
npx kkpan-cli --help
```

### 3. Linux / macOS 一键安装

```bash
curl -fsSL https://raw.githubusercontent.com/A-510/kkpan-cli/main/install.sh | bash
```

### 4. Antigravity AI Agent 插件

仓库内置 [plugins/kkpan](./plugins/kkpan) 插件包（包含 `plugin.json`、`rules/AGENTS.md`、完整 `SKILL.md` 及跨平台二进制解析脚本），可复制至 `~/.gemini/config/plugins/kkpan/` 在 Antigravity 中直接启用。

### 5. 从源码编译

需要 Go 1.25+：

```bash
go build -o kkpan .
```

Windows PowerShell 示例：

```powershell
go build -o kkpan.exe .
```

---

## 打包发布与隐私安全检查

> 本地账号会话保存在 `%APPDATA%\kkpan\kkpan_config.json`（或 `~/.config/kkpan/kkpan_config.json`），已被 `.gitignore` 与 `.npmignore` 双重隔离。在推送 Git 或发布 npm/bun 前，**务必先运行隐私自检脚本**：

```powershell
# 1. 发布前隐私与打包清单自检（拦截 Cookie、个人路径、敏感配置文件）
.\scripts\privacy-check.ps1

# 2. 同步版本号（main.go / package.json / plugin.json）
node .\scripts\sync-version.mjs 0.1.0

# 3. 交叉编译全部 6 平台发行包与 SHA256SUMS 至 dist/
.\scripts\release.ps1 -Version v0.1.0
```

---

## 快速开始

```bash
# 1. 登录夸克网盘（推荐终端扫码）
kkpan login -qr
# 或粘贴 pan.quark.cn Cookie
kkpan login -cookies "__pus=...; __puus=..."

# 2. 启动交互式 REPL 或全屏日式二次元网盘浏览器
kkpan
kkpan ui

# 3. 常用命令
kkpan who --json                   # 查看当前账号（JSON）
kkpan quota                        # 查看容量与会员等级
kkpan ls /                         # 列出根目录
kkpan cd /我的资料                  # 切换工作目录
kkpan tree --depth 2 /             # 树形查看目录
kkpan d /电影/demo.mp4 -p 2         # 下载文件
kkpan u ./archive.zip /备份        # 上传文件（优先秒传）
kkpan sumfile ./archive.zip        # 计算本地 MD5 与 SHA1

# 4. BT 种子与磁力链接解析下载（支持选文件与自动上传网盘）
kkpan bt --list ./movie.torrent                        # 预览种子内文件清单
kkpan bt --files 1,3-5 ./pack.torrent                  # 仅下载指定序号文件
kkpan bt --match "*.mp4" "magnet:?xt=urn:btih:..."     # 按通配符筛选磁力文件
kkpan bt --all --upload /离线资源 ./demo.torrent       # 下载完成后自动上传到夸克网盘

# 5. 分享转存与回收站
kkpan tf --list https://pan.quark.cn/s/xxxx         # 预览分享目录
kkpan tf --to /资源 --collect https://pan.quark.cn/s/xxxx 1a2b
kkpan share set -p 1a2b -period 7 -f /我的资料       # 创建 7 天有效加密分享
kkpan share list --json                             # 列出我的分享
kkpan recycle list                                  # 查看回收站
kkpan recycle restore <record_id>                   # 还原指定回收站记录
```

---

## 常用配置与环境变量

配置文件默认位于 `~/.config/kkpan/kkpan_config.json`（Windows 为 `%APPDATA%\kkpan\kkpan_config.json`，权限 `0600`）：

- `KKPAN_CONFIG_DIR`：覆盖默认配置文件目录
- `KKPAN_THEME`：预设主题（`celestial` / `dossier` / `shinbo`）
- `KKPAN_PROMPT_STYLE`：REPL 提示符风格（`starship` / `boxed`）
- `KKPAN_REDUCE_MOTION=1`：直接启动 REPL 时显示静态吉祥物。
- `KKPAN_ASCII=1`：强制纯 ASCII 符号渲染
- `NO_COLOR`：禁用彩色输出

```bash
kkpan config                       # 查看当前配置（不含敏感 Cookie）
kkpan config set -theme shinbo -max_parallel 2 -max_download_rate 20MB/s
```

---

## 分层架构

```text
main.go
  └── internal/cliapp        # 命令注册、全局 Flag 预扫描、liner REPL
        ├── internal/tui     # Bubble Tea 日式二次元双栏全屏档案浏览器
        ├── internal/kkcommand # 业务命令编排
        ├── internal/ui      # 三套二次元主题、表格、星轨进度条、二维码、JSON/TSV
        ├── internal/kkconfig# 多账号配置持久化 (0600) 与 Cookie 自动续期回调
        └── internal/transfer# 多线程分片断点下载、流式秒传与 OSS 分片上传、令牌桶限速
              │
              ▼
        quarkpan             # 纯夸克网盘协议 SDK（PathResolver / TaskPoller / Auth / File / OSS）
```

---

## 致谢

- CLI 分层结构、命令行参数风格与终端用户体验思路参考自开源项目 [qwerprog/pan-tools](https://github.com/qwerprog/pan-tools)（Apache-2.0 License）。
- 全屏 TUI 视觉配色与排版体系基于 `design-pipeline` 技能规范设计。
