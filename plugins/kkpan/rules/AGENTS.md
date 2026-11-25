# kkpan 夸克网盘 Agent 安全与调用规范

- **结构化与非交互调用**：在调用会输出列表或元信息的命令（`who`、`loglist`、`quota`、`ls`、`search`、`meta`、`locate`、`share list`、`recycle list`、`transfer --list`）时，必须在子命令前传入 `-q --no-color`，并在子命令后传入 `--json`（或 `--plain`）。严禁在非交互工具调用中启动 `kkpan`（无参数 REPL）、`kkpan ui`/`tui`/`browse` 或无参数 `kkpan login`。
- **破坏性与外泄操作二次确认**：执行任何删除、覆盖、清空或对外公开分享命令（`rm`、`recycle delete`、`recycle delete --all`、`share set`、`share cancel`、`logout`、`config reset`、`download --ow`、`upload --policy overwrite`）之前，必须先向用户展示精确的目标网盘路径、FID、Share ID 或 Record ID 并获得明确确认。
- **凭据与隐私红线**：严禁读取、打印、记录或转发用户的 `Cookie`（`__pus`、`__puus` 等）或本地配置文件（`kkpan_config.json`、`%APPDATA%\kkpan\kkpan_config.json`、`~/.config/kkpan/kkpan_config.json`）。如果 `who --json` 返回 `logged_in == false` 或 `uid == ""`，必须指引用户在自己的终端运行 `kkpan login -qr` 扫码登录，切勿要求用户在对话中粘贴 Cookie。
- **速率与风控保护**：对于非 VIP / SVIP 账号，不要擅自将单文件下载线程 `-p` 或并行任务数 `-l` 调高超过 `1`，除非用户明确要求。
