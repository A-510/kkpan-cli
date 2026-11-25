#!/usr/bin/env node
// kkpan 跨运行时 CLI 入口（同时兼容 npm/npx 与 bun/bunx，完整保留 TTY 与退出码）
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { spawnSync } from "node:child_process";
import {
  DEFAULT_REPO,
  downloadAndInstallBinary,
  findExistingBinary,
} from "../scripts/platform.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const pkgRoot = path.resolve(__dirname, "..");

async function ensureBinary() {
  const existing = findExistingBinary(pkgRoot);
  if (existing) {
    return existing;
  }

  const pkg = JSON.parse(fs.readFileSync(path.join(pkgRoot, "package.json"), "utf8"));
  console.error(`[kkpan-cli] 正在初始化本地二进制 (v${pkg.version})...`);
  try {
    return await downloadAndInstallBinary(pkgRoot, pkg.version);
  } catch (err) {
    console.error(`[kkpan-cli] 无法自动下载 kkpan 二进制: ${err.message}`);
    console.error(`[kkpan-cli] 请使用以下任一方式手动安装，或设置环境变量 KKPAN_BIN 指向可执行文件：`);
    console.error(`  - Windows PowerShell: irm https://raw.githubusercontent.com/${DEFAULT_REPO}/main/install.ps1 | iex`);
    console.error(`  - macOS / Linux:      curl -fsSL https://raw.githubusercontent.com/${DEFAULT_REPO}/main/install.sh | bash`);
    process.exit(1);
  }
}

const binPath = await ensureBinary();
const res = spawnSync(binPath, process.argv.slice(2), {
  stdio: "inherit",
  windowsHide: false,
});

if (res.error) {
  console.error(`[kkpan-cli] 启动失败 (${binPath}): ${res.error.message}`);
  process.exit(1);
}
if (res.signal) {
  process.kill(process.pid, res.signal);
}
process.exit(res.status ?? 0);
