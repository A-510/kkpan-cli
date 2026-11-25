// 跨平台解析与按需下载 kkpan 原生二进制（零第三方依赖，兼容 Node.js 18+ 与 Bun）
import fs from "node:fs";
import path from "node:path";
import os from "node:os";
import crypto from "node:crypto";
import { execFileSync } from "node:child_process";

export const DEFAULT_REPO = process.env.KKPAN_REPO || "A-510/kkpan-cli";

export function resolveTarget() {
  const platformMap = {
    win32: { goOS: "windows", ext: "zip", bin: "kkpan.exe" },
    darwin: { goOS: "darwin", ext: "zip", bin: "kkpan" },
    linux: { goOS: "linux", ext: "tar.gz", bin: "kkpan" },
  };
  const archMap = {
    x64: "amd64",
    arm64: "arm64",
  };

  const p = platformMap[process.platform];
  const a = archMap[process.arch];
  if (!p || !a) {
    throw new Error(`暂不支持当前系统平台: ${process.platform}/${process.arch}`);
  }
  return {
    goOS: p.goOS,
    goArch: a,
    ext: p.ext,
    binaryName: p.bin,
  };
}

export function getCacheBinaryPath(target = resolveTarget()) {
  if (process.platform === "win32") {
    const localApp = process.env.LOCALAPPDATA || path.join(os.homedir(), "AppData", "Local");
    return path.join(localApp, "kkpan-cli", "bin", target.binaryName);
  }
  const cacheHome = process.env.XDG_CACHE_HOME || path.join(os.homedir(), ".cache");
  return path.join(cacheHome, "kkpan-cli", "bin", target.binaryName);
}

export function getVendorBinaryPath(pkgRoot, target = resolveTarget()) {
  return path.join(pkgRoot, "vendor", target.binaryName);
}

export function isExecutable(filePath) {
  try {
    const st = fs.statSync(filePath);
    return st.isFile() && st.size > 0;
  } catch {
    return false;
  }
}

export function findExistingBinary(pkgRoot, target = resolveTarget()) {
  const envBin = (process.env.KKPAN_BIN || "").trim();
  if (envBin && isExecutable(envBin)) {
    return envBin;
  }

  const vendorBin = getVendorBinaryPath(pkgRoot, target);
  if (isExecutable(vendorBin)) {
    return vendorBin;
  }

  const cacheBin = getCacheBinaryPath(target);
  if (isExecutable(cacheBin)) {
    return cacheBin;
  }

  // 本地源码调试时直接复用仓库根目录编译出的二进制
  const devBin = path.join(pkgRoot, target.binaryName);
  if (isExecutable(devBin)) {
    return devBin;
  }

  return "";
}

export function normalizeTag(rawVersion) {
  const v = String(rawVersion || "").trim();
  if (!v) {
    return "v0.1.0";
  }
  return v.startsWith("v") ? v : `v${v}`;
}

export function buildDownloadURLs(version, target = resolveTarget()) {
  const tag = normalizeTag(version);
  const pkgName = `kkpan-${tag}-${target.goOS}-${target.goArch}.${target.ext}`;
  const customBase = (process.env.KKPAN_RELEASE_BASE_URL || "").trim().replace(/\/+$/, "");
  const baseURL = customBase || `https://github.com/${DEFAULT_REPO}/releases/download/${tag}`;
  return {
    tag,
    pkgName,
    archiveURL: `${baseURL}/${pkgName}`,
    checksumURL: `${baseURL}/SHA256SUMS`,
  };
}

async function fetchBuffer(url) {
  const res = await fetch(url, {
    redirect: "follow",
    headers: { "User-Agent": "kkpan-cli-installer" },
  });
  if (!res.ok) {
    throw new Error(`HTTP ${res.status} (${url})`);
  }
  const ab = await res.arrayBuffer();
  return Buffer.from(ab);
}

async function fetchOptionalText(url) {
  try {
    const res = await fetch(url, {
      redirect: "follow",
      headers: { "User-Agent": "kkpan-cli-installer" },
    });
    if (!res.ok) {
      return "";
    }
    return await res.text();
  } catch {
    return "";
  }
}

function verifySHA256IfPresent(archiveBuf, pkgName, sumsText) {
  if (!sumsText) {
    return false;
  }
  const lines = sumsText.split(/\r?\n/);
  for (const line of lines) {
    const m = line.match(/^\s*([a-fA-F0-9]{64})\s+\*?(.+?)\s*$/);
    if (m && m[2].trim() === pkgName) {
      const expected = m[1].toLowerCase();
      const actual = crypto.createHash("sha256").update(archiveBuf).digest("hex").toLowerCase();
      if (actual !== expected) {
        throw new Error(`SHA256 校验失败: ${pkgName} 期望 ${expected}，实际 ${actual}`);
      }
      return true;
    }
  }
  return false;
}

function findFileRecursive(dir, targetName) {
  const entries = fs.readdirSync(dir, { withFileTypes: true });
  for (const ent of entries) {
    const full = path.join(dir, ent.name);
    if (ent.isFile() && ent.name === targetName) {
      return full;
    }
    if (ent.isDirectory()) {
      const nested = findFileRecursive(full, targetName);
      if (nested) {
        return nested;
      }
    }
  }
  return "";
}

function extractArchive(archivePath, outDir, target) {
  if (target.ext === "tar.gz") {
    execFileSync("tar", ["-xzf", archivePath, "-C", outDir], { stdio: "pipe" });
    return;
  }
  if (process.platform === "win32") {
    const psCmd = `Expand-Archive -LiteralPath '${archivePath.replace(/'/g, "''")}' -DestinationPath '${outDir.replace(/'/g, "''")}' -Force`;
    execFileSync("powershell", ["-NoProfile", "-ExecutionPolicy", "Bypass", "-Command", psCmd], { stdio: "pipe" });
    return;
  }
  execFileSync("unzip", ["-oq", archivePath, "-d", outDir], { stdio: "pipe" });
}

// 下载预编译产物并安装到可写目录（优先包内 vendor/，若无权限则自动落盘到用户缓存目录）
export async function downloadAndInstallBinary(pkgRoot, version) {
  const target = resolveTarget();
  const { pkgName, archiveURL, checksumURL } = buildDownloadURLs(version, target);

  const archiveBuf = await fetchBuffer(archiveURL);
  const sumsText = await fetchOptionalText(checksumURL);
  verifySHA256IfPresent(archiveBuf, pkgName, sumsText);

  const tmpDir = fs.mkdtempSync(path.join(os.tmpdir(), "kkpan-dl-"));
  try {
    const archivePath = path.join(tmpDir, pkgName);
    const unpackDir = path.join(tmpDir, "unpack");
    fs.mkdirSync(unpackDir, { recursive: true });
    fs.writeFileSync(archivePath, archiveBuf);

    extractArchive(archivePath, unpackDir, target);
    const extracted = findFileRecursive(unpackDir, target.binaryName);
    if (!extracted) {
      throw new Error(`解压后未找到二进制文件 ${target.binaryName}`);
    }

    const candidates = [getVendorBinaryPath(pkgRoot, target), getCacheBinaryPath(target)];
    let lastErr = null;
    for (const dest of candidates) {
      try {
        fs.mkdirSync(path.dirname(dest), { recursive: true });
        const tmpDest = `${dest}.tmp-${process.pid}`;
        fs.copyFileSync(extracted, tmpDest);
        if (process.platform !== "win32") {
          fs.chmodSync(tmpDest, 0o755);
        }
        fs.renameSync(tmpDest, dest);
        return dest;
      } catch (err) {
        lastErr = err;
      }
    }
    throw lastErr || new Error("写入 kkpan 二进制文件失败");
  } finally {
    fs.rmSync(tmpDir, { recursive: true, force: true });
  }
}
