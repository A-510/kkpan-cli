# kkpan Antigravity 插件

将夸克网盘 CLI 客户端 `kkpan` 接入 Antigravity，提供安全的非交互结构化操作规范、凭据保护规则与跨平台自动探测脚本。

## 安装方式

- **Windows (PowerShell `irm`)**:
  ```powershell
  irm https://raw.githubusercontent.com/A-510/kkpan-cli/main/install.ps1 | iex
  ```
- **npm / bun**:
  ```bash
  npm i -g kkpan-cli
  # 或使用 bun
  bun add -g kkpan-cli
  # 或免安装直接运行
  bunx kkpan-cli --help
  ```
- **Linux / macOS**:
  ```bash
  curl -fsSL https://raw.githubusercontent.com/A-510/kkpan-cli/main/install.sh | bash
  ```

## 登录授权

在自己的交互式终端中执行以下命令扫码登录（请勿在聊天框粘贴 Cookie）：

```bash
kkpan login -qr
```
