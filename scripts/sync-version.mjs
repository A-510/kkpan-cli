// 一键同步版本号到 main.go、package.json 与 plugins/kkpan/plugin.json
// 用法: node ./scripts/sync-version.mjs 0.1.0  (或 bun ./scripts/sync-version.mjs v0.1.0)
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(__dirname, "..");

const rawArg = (process.argv[2] || "").trim();
if (!rawArg) {
  console.error("用法: node ./scripts/sync-version.mjs <version>  (例如 0.1.0 或 v0.1.0)");
  process.exit(1);
}

const cleanVer = rawArg.replace(/^v/i, "");
if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(cleanVer)) {
  console.error(`无效的语义化版本号: ${rawArg}`);
  process.exit(1);
}
const tagVer = `v${cleanVer}`;

// 1. 更新 main.go
const mainGoPath = path.join(root, "main.go");
const mainGo = fs.readFileSync(mainGoPath, "utf8");
const updatedMainGo = mainGo.replace(
  /(var\s+Version\s*=\s*")([^"]*)(")/,
  `$1${tagVer}$3`
);
if (updatedMainGo === mainGo && !mainGo.includes(`"${tagVer}"`)) {
  console.error("未能在 main.go 中匹配到 Version 变量定义");
  process.exit(1);
}
fs.writeFileSync(mainGoPath, updatedMainGo, "utf8");

// 2. 更新 package.json
const pkgPath = path.join(root, "package.json");
const pkg = JSON.parse(fs.readFileSync(pkgPath, "utf8"));
pkg.version = cleanVer;
fs.writeFileSync(pkgPath, JSON.stringify(pkg, null, 2) + "\n", "utf8");

// 3. 更新 plugins/kkpan/plugin.json
const pluginPath = path.join(root, "plugins", "kkpan", "plugin.json");
if (fs.existsSync(pluginPath)) {
  const plugin = JSON.parse(fs.readFileSync(pluginPath, "utf8"));
  plugin.version = cleanVer;
  fs.writeFileSync(pluginPath, JSON.stringify(plugin, null, 2) + "\n", "utf8");
}

console.log(`已将 main.go (${tagVer})、package.json (${cleanVer})、plugins/kkpan/plugin.json (${cleanVer}) 同步一致。`);
