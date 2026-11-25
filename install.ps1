# kkpan-cli (kkpan) - Windows 独立一键安装脚本
# 用法 (PowerShell):
#   irm https://raw.githubusercontent.com/A-510/kkpan-cli/main/install.ps1 | iex
#   $env:KKPAN_VERSION="v0.1.0"; irm https://raw.githubusercontent.com/A-510/kkpan-cli/main/install.ps1 | iex

$ErrorActionPreference = "Stop"

try {
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12
} catch {
}

$repo = if ($env:KKPAN_REPO) { $env:KKPAN_REPO.Trim() } else { "A-510/kkpan-cli" }
$binaryName = "kkpan.exe"

function Write-Info($msg) {
    Write-Host "==> " -ForegroundColor Cyan -NoNewline
    Write-Host $msg -ForegroundColor White
}

function Write-Success($msg) {
    Write-Host "[OK] " -ForegroundColor Green -NoNewline
    Write-Host $msg -ForegroundColor White
}

function Write-WarnMsg($msg) {
    Write-Host "[!] " -ForegroundColor Yellow -NoNewline
    Write-Host $msg -ForegroundColor Yellow
}

# 1. 检测系统架构
if (-not [System.Environment]::Is64BitOperatingSystem) {
    throw "不支持 32 位操作系统，仅支持 64 位 (amd64 / arm64) Windows。"
}

$arch = "amd64"
if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64" -or $env:PROCESSOR_ARCHITEW6432 -eq "ARM64") {
    $arch = "arm64"
}

# 2. 解析版本号
$version = $env:KKPAN_VERSION
if (-not $version) {
    Write-Info "正在从 GitHub ($repo) 获取最新版本..."
    try {
        $req = [System.Net.WebRequest]::Create("https://github.com/$repo/releases/latest")
        $req.AllowAutoRedirect = $false
        $resp = $req.GetResponse()
        $loc = $resp.GetResponseHeader("Location")
        $resp.Close()
        if ($loc -match "/tag/([^/]+)$") {
            $version = $Matches[1]
        }
    } catch {
    }

    if (-not $version) {
        try {
            $release = Invoke-RestMethod -Uri "https://api.github.com/repos/$repo/releases/latest" -Headers @{ "User-Agent" = "kkpan-installer" }
            $version = $release.tag_name
        } catch {
            throw "无法获取最新版本号，请确认已发布 Release 或手动指定版本: `$env:KKPAN_VERSION='v0.1.0'; irm https://raw.githubusercontent.com/$repo/main/install.ps1 | iex"
        }
    }
}

$version = $version.Trim()
if (-not $version.StartsWith("v")) {
    $version = "v$version"
}

$archiveDir = "kkpan-$version-windows-$arch"
$pkgName = "$archiveDir.zip"
$baseDownload = if ($env:KKPAN_RELEASE_BASE_URL) {
    $env:KKPAN_RELEASE_BASE_URL.TrimEnd('/')
} else {
    "https://github.com/$repo/releases/download/$version"
}
$downloadUrl = "$baseDownload/$pkgName"
$checksumUrl = "$baseDownload/SHA256SUMS"

Write-Info "检测到运行环境: windows-$arch"
Write-Info "准备安装 kkpan $version..."

# 3. 下载、校验并解压
$tempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("kkpan-" + [System.Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tempDir -Force | Out-Null

try {
    $zipPath = Join-Path $tempDir $pkgName
    Write-Info "正在下载 $downloadUrl..."
    Invoke-WebRequest -Uri $downloadUrl -OutFile $zipPath -UseBasicParsing

    # 若 Release 附带 SHA256SUMS 则自动执行完整性校验
    $sumsPath = Join-Path $tempDir "SHA256SUMS"
    $verified = $false
    try {
        Invoke-WebRequest -Uri $checksumUrl -OutFile $sumsPath -UseBasicParsing
        $lines = Get-Content -Path $sumsPath -Encoding UTF8 -ErrorAction SilentlyContinue
        foreach ($line in $lines) {
            if ($line -match '^\s*([A-Fa-f0-9]{64})\s+\*?(.+?)\s*$') {
                $expectedHash = $Matches[1].ToLowerInvariant()
                $targetName = $Matches[2].Trim()
                if ($targetName -eq $pkgName) {
                    $actualHash = (Get-FileHash -Path $zipPath -Algorithm SHA256).Hash.ToLowerInvariant()
                    if ($actualHash -ne $expectedHash) {
                        throw "SHA256 校验失败: $pkgName 期望 $expectedHash，实际 $actualHash"
                    }
                    $verified = $true
                    break
                }
            }
        }
    } catch {
        if ($_.Exception.Message -like "SHA256 校验失败*") {
            throw
        }
    }
    if ($verified) {
        Write-Success "SHA256 校验通过 ($pkgName)"
    } else {
        Write-WarnMsg "未在 Release 中找到 $pkgName 的 SHA256SUMS 记录，跳过哈希校验"
    }

    Write-Info "正在解压安装包..."
    Expand-Archive -Path $zipPath -DestinationPath $tempDir -Force

    $srcExe = Join-Path (Join-Path $tempDir $archiveDir) $binaryName
    if (-not (Test-Path $srcExe)) {
        $srcExe = Join-Path $tempDir $binaryName
    }
    if (-not (Test-Path $srcExe)) {
        $found = Get-ChildItem -Path $tempDir -Filter $binaryName -Recurse -File | Select-Object -First 1
        if ($found) {
            $srcExe = $found.FullName
        }
    }
    if (-not (Test-Path $srcExe)) {
        throw "在安装包中未找到可执行文件 '$binaryName'。"
    }

    # 4. 安装到目标目录
    $installDir = if ($env:KKPAN_INSTALL_DIR) {
        $env:KKPAN_INSTALL_DIR
    } else {
        Join-Path $env:LOCALAPPDATA "Programs\kkpan"
    }

    if (-not (Test-Path $installDir)) {
        New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    }

    $destExe = Join-Path $installDir $binaryName
    Copy-Item -Path $srcExe -Destination $destExe -Force

    Write-Success "kkpan $version 已成功安装到: $destExe"

    # 5. 幂等写入用户级 PATH 环境变量
    $userPath = [System.Environment]::GetEnvironmentVariable("Path", "User")
    $pathParts = @()
    if ($userPath) {
        $pathParts = @($userPath -split ";" | Where-Object { $_ -ne "" })
    }

    $normalizedTarget = $installDir.TrimEnd('\')
    $alreadyInPath = $false
    foreach ($p in $pathParts) {
        if ($p.TrimEnd('\') -ieq $normalizedTarget) {
            $alreadyInPath = $true
            break
        }
    }

    if (-not $alreadyInPath) {
        Write-Info "正在将安装路径添加到用户 PATH 环境变量..."
        $newPath = ($pathParts + $installDir) -join ";"
        [System.Environment]::SetEnvironmentVariable("Path", $newPath, "User")
        $env:Path = ($env:Path.TrimEnd(";") + ";" + $installDir)
        Write-Success "已将 $installDir 添加至用户 PATH。"
    }

    Write-Host ""
    Write-Host "执行 'kkpan --help' 或 'kkpan ui' 即可开始使用！（若旧终端未刷新，请新开终端窗口）" -ForegroundColor Green
} finally {
    if (Test-Path $tempDir) {
        Remove-Item -Path $tempDir -Recurse -Force -ErrorAction SilentlyContinue
    }
}
