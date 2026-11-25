// npm/bun install 完成后自动拉取对应平台预编译二进制（下载失败不阻断离线安装）
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  DEFAULT_REPO,
  downloadAndInstallBinary,
  findExistingBinary,
} from "./platform.mjs";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const pkgRoot = path.resolve(__dirname, "..");

async function main() {
  if (process.env.KKPAN_SKIP_DOWNLOAD === "1") {
    return;
  }

  const existing = findExistingBinary(pkgRoot);
  if (existing) {
    return;
  }

  const pkg = JSON.parse(fs.readFileSync(path.join(pkgRoot, "package.json"), "utf8"));
  try {
    const installedPath = await downloadAndInstallBinary(pkgRoot, pkg.version);
    console.error(`[kkpan-cli] 已就绪: ${installedPath}`);
  } catch (err) {
    console.error(`[kkpan-cli] 预安装下载跳过 (${err.message})，首次运行 kkpan 时会自动重试。`);
    console.error(`[kkpan-cli] 若网络受限，可改用独立安装脚本或手动下载 Release:`);
    console.error(`  Windows: irm https://raw.githubusercontent.com/${DEFAULT_REPO}/main/install.ps1 | iex`);
    console.error(`  macOS/Linux: curl -fsSL https://raw.githubusercontent.com/${DEFAULT_REPO}/main/install.sh | bash`);
  }
}

await main();
