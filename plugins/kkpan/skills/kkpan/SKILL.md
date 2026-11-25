---
name: kkpan
description: >-
  通过 `kkpan` Go CLI 在夸克网盘中浏览目录、搜索文件、多线程断点下载、秒传/OSS 分片上传、
  提取直链、转存分享链接/口令、管理分享与回收站以及切换多账号或查看配额。
  仅在用户操作夸克网盘时使用；不要用于百度网盘（用 pan-tools）、Google Drive、Dropbox 或纯本地文件系统任务。
metadata:
  icon: ☁️
---

# kkpan (夸克网盘 CLI)

使用 `kkpan` 以非交互、结构化且安全的方式执行夸克网盘读写与管理任务。

## 前置检查（Pre-Flight Check）

1. 先检查系统 `PATH` 中是否已有 `kkpan`，或调用内置跨平台解析脚本 `<SKILL_DIR>/scripts/kkpan.cmd`（Windows） / `<SKILL_DIR>/scripts/kkpan.sh`（Linux/macOS）——它会自动按 `$KKPAN_BIN` → `PATH` → `bun`/`npm` 全局或本地缓存 → 从 `A-510/kkpan-cli` Releases 拉取二进制的顺序定位可用程序：
   ```powershell
   & "<SKILL_DIR>\scripts\kkpan.cmd" --version
   ```
   ```bash
   <SKILL_DIR>/scripts/kkpan.sh --version
   ```
   如需指导用户手动安装：
   - **Windows PowerShell (`irm`)**：`irm https://raw.githubusercontent.com/A-510/kkpan-cli/main/install.ps1 | iex`
   - **npm / bun**：`npm i -g kkpan-cli` 或 `bun add -g kkpan-cli`（或免安装 `bunx kkpan-cli ...`）
   - **Linux / macOS**：`curl -fsSL https://raw.githubusercontent.com/A-510/kkpan-cli/main/install.sh | bash`

2. 执行任何业务指令前，先以结构化只读方式检测登录状态：
   ```bash
   kkpan -q --no-color who --json
   ```
   - `who --json` 即使未登录也会返回退出码 `0`（`{"logged_in":false,...}`）。务必确认 `logged_in == true` 且 `uid != ""`。
   - 若未登录，**严禁**执行 `kkpan login` 裸交互命令或读取本地 `kkpan_config.json`；应提示用户在自己的终端执行 `kkpan login -qr` 用手机夸克 App 扫码登录。

## 传参顺序规范（`urfave/cli/v2`）

- 全局标志（`-q` / `--quiet`、`--no-color`、`--ascii`、`--theme`、`--prompt-style`）必须放在**子命令之前**，例如：`kkpan -q --no-color ls --json /`。
- 子命令自身标志（`--json`、`--plain`、`--fid`、`--to`、`--collect`、`--download`、`--saveto`、`--policy` 等）放在**子命令之后**。

## 常用工作流

### 1. 账号与容量配额
```bash
kkpan -q --no-color who --json
kkpan -q --no-color loglist --json
kkpan -q --no-color quota --json
kkpan -q --no-color su <uid_or_nickname>
```

### 2. 浏览、搜索、元信息与目录维护
```bash
kkpan -q --no-color ls --json /
kkpan -q --no-color ls --fid --plain /我的资料
kkpan -q --no-color tree --depth 2 /
kkpan -q --no-color search --json "关键字"
kkpan -q --no-color meta --json /路径/文件.pdf
kkpan -q --no-color cd /工作目录
kkpan -q --no-color mkdir /目标目录/子目录
kkpan -q --no-color cp /源路径 /目标目录
kkpan -q --no-color mv /源路径 /目标目录
kkpan -q --no-color rename /源路径 新名称
# 执行 rm 移入回收站前，必须先让用户确认精确路径：
kkpan -q --no-color rm /待删除路径
```

> 若同目录下存在重名或路径包含特殊字符，可使用 `fid:<文件FID>` 直连语法精确指定目标（如 `kkpan -q --no-color meta --json fid:abc123`）。

### 3. 下载、直链提取与上传
```bash
# 下载（仅当用户明确同意覆盖本地文件时才加 --ow）
kkpan -q --no-color download --saveto ./downloads --mtime /远程/文件.pdf
# 提取临时下载直链
kkpan -q --no-color locate --json /远程/文件.pdf
# 上传（支持秒传与 OSS 分片断点续传；--policy: skip | overwrite | rename）
kkpan -q --no-color upload --policy skip ./local.zip /网盘目标目录
```

### 4. 分享转存（`transfer` / `tf`）
支持免密链接、带 `?pwd=` 链接、独立提取码参数以及整段中文夸克分享口令：
```bash
# 仅预览分享内文件列表（只读）
kkpan -q --no-color transfer --list --json "https://pan.quark.cn/s/xxxx"
# 转存到指定目录，--collect 按分享标题建独立目录，--download 转存后立即下载
kkpan -q --no-color transfer --to /资源 --collect "https://pan.quark.cn/s/xxxx" "1a2b"
kkpan -q --no-color transfer --json --to /资源 "我用夸克网盘分享了... 链接：https://pan.quark.cn/s/xxxx 提取码：1a2b"
```

### 5. 分享管理与回收站
```bash
# 查看我的分享（只读）
kkpan -q --no-color share list --json
# 创建/取消分享前必须先获得用户明确确认：
kkpan -q --no-color share set -p 1a2b --period 7 -f /我的资料
kkpan -q --no-color share cancel <share_id>

# 查看回收站（只读）
kkpan -q --no-color recycle list --json
kkpan -q --no-color recycle restore <record_id>
# 彻底删除或 --all 清空前必须先经用户明确确认：
kkpan -q --no-color recycle delete <record_id>
```

### 6. 配置与校验工具
```bash
kkpan -q --no-color config
kkpan -q --no-color config set -max_download_rate 20MB/s -max_download_load 3
kkpan -q --no-color env
kkpan -q --no-color sumfile ./archive.zip
```

## 完整参考文档
如需查看完整子命令标志、配置键、环境变量与 JSON/TSV 字段说明，请阅读 [references/commands.md](./references/commands.md)。
