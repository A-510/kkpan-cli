# kkpan 命令与结构化输出参考手册

## 1. 全局标志（必须放在子命令之前）

| 标志 | 简写 | 环境变量 | 说明 |
| :--- | :--- | :--- | :--- |
| `--quiet` | `-q` | - | 静默模式，隐藏横幅和进度条（Agent 调用必带） |
| `--no-color` | - | `NO_COLOR` | 禁用 ANSI 转义色（Agent 调用必带） |
| `--ascii` | - | `KKPAN_ASCII=1` | 纯 7-bit ASCII 符号边框与降级显示 |
| `--theme` | - | `KKPAN_THEME` | 主题：`celestial` / `dossier` / `shinbo` |
| `--prompt-style` | - | `KKPAN_PROMPT_STYLE` | REPL 提示符风格：`starship` / `boxed` |

其他环境变量：
- `KKPAN_CONFIG_DIR`：覆盖配置文件目录（默认 Windows 为 `%APPDATA%\kkpan`，Linux/macOS 为 `~/.config/kkpan`）。
- `KKPAN_BIN`：覆盖二进制可执行文件路径。

## 2. 子命令树与结构化输出支持

| 命令 / 别名 | 支持 `--json` / `--plain` | 核心标志 | 说明 |
| :--- | :--- | :--- | :--- |
| `who` | 是 | - | 返回 `uid`, `nickname`, `member_type`, `workdir`, `logged_in`（不含 Cookie） |
| `loglist` | 是 | - | 返回已登录账号列表摘要 |
| `quota` | 是 | - | 返回 `use_capacity`, `total_capacity`, `percent`, `member_type`, `is_vip`, `is_svip` |
| `su <uid\|昵称>` | 否 | - | 切换活跃账号 |
| `logout [uid\|昵称]` | 否 | - | 注销账号凭证（高危，需确认） |
| `ls`, `l`, `ll` | 是 | `--fid`, `--time`, `--size`, `--name`, `--desc`, `--refresh` | 列举目录；返回 `FileItem[]` |
| `cd <路径>` / `pwd` | 否 | - | 切换/打印当前工作目录 |
| `tree [目录]` | 否 | `--depth`, `--fid` | 树形预览目录 |
| `search`, `s <词>` | 是 | `--page`, `--size` | 搜索全盘文件与目录 |
| `meta <路径>` | 是 | - | 查看文件/目录 FID、MD5 与大小 |
| `mkdir` / `rename` / `mv` / `cp` / `rm` | 否 | - | 目录与文件操作（`rm` 移入回收站，需确认） |
| `locate <路径...>` | 是 | - | 提取文件直链（含 `download_url`, `referer`, `user_agent`，不含 Cookie） |
| `download`, `d` | 否 | `--saveto`, `-p`, `-l`, `--ow`, `--nocheck`, `--mtime`, `--fullpath`, `--test` | 多线程 Range 分片断点下载 |
| `upload`, `u` | 否 | `--policy` (`skip`/`overwrite`/`rename`), `-p`, `-l`, `--no-rapid` | 单遍流式秒传 + OSS 分片上传 |
| `transfer`, `tf` | 是 | `--to`, `--collect`, `--download`, `--list`, `--fid`, `--pdir` | 分享链接/口令转存 |
| `share set` | 是 | `-p`/`--passcode`, `--period` (`0/1/7/30`), `--title`, `-f`/`--full` | 创建分享链接（需确认） |
| `share list` | 是 | `--page`, `--size` | 查看我的分享与 PV 统计 |
| `share cancel` | 否 | - | 取消指定 `share_id` 的分享（需确认） |
| `recycle list` | 是 | `--page`, `--size` | 查看回收站记录与 `record_id` |
| `recycle restore` | 否 | - | 按 `record_id` 还原回收站项 |
| `recycle delete` | 否 | `--all` | 按 `record_id` 彻底删除或 `--all` 清空（高危，需确认） |
| `config` / `config set` / `config reset` | 否 | 各配置项同名 flag 或 `k=v` | 配置查询与修改 |
| `env` / `sumfile` | 否 | - | 环境变量状态 / 计算本地 MD5+SHA1 |

## 3. 配置项列表（`kkpan config set`）

- `savedir`：默认本地下载目录
- `max_parallel`：单文件下载分片线程数
- `max_upload_parallel`：单文件上传分片并发数
- `max_download_load`：同时下载文件数量（默认 `3`）
- `max_upload_load`：同时上传文件数量（默认 `2`）
- `max_download_rate` / `max_upload_rate`：全局限速（如 `20MB/s` 或 `0`）
- `u_policy`：上传重名策略（`skip` / `overwrite` / `rename`）
- `no_check`：禁用下载 MD5 校验（`true` / `false`）
- `modify_mtime`：同步服务端修改时间（`true` / `false`）
- `proxy`：HTTP / SOCKS5 代理地址
- `theme`：`celestial` / `dossier` / `shinbo`
- `prompt_style`：`starship` / `boxed`
