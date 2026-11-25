# kkpan-cli 多平台交叉编译与打包脚本 (Windows PowerShell)
# 用法示例:
#   .\scripts\release.ps1
#   .\scripts\release.ps1 -Version v0.1.0
#   .\scripts\release.ps1 -Version v0.1.0 -Targets windows/amd64,linux/amd64
param(
    [string]$Version = "",
    [string]$Targets = "windows/amd64,windows/arm64,darwin/amd64,darwin/arm64,linux/amd64,linux/arm64",
    [switch]$SkipPrivacyCheck
)

$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
Push-Location $repoRoot

try {
    if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
        throw "未在 PATH 中找到 go 命令，请先安装 Go 1.25+ 并配置 PATH。"
    }

    # 1. 发布前隐私安全检查
    if (-not $SkipPrivacyCheck) {
        $privacyScript = Join-Path $PSScriptRoot "privacy-check.ps1"
        & $privacyScript -SkipNpmPack
        if ($LASTEXITCODE -ne 0) {
            throw "隐私安全自检未通过，已中止打包。"
        }
    }

    # 2. 解析版本号
    if (-not $Version) {
        $mainGo = Get-Content -Path (Join-Path $repoRoot "main.go") -Raw -Encoding UTF8
        if ($mainGo -match 'Version\s*=\s*"([^"]+)"') {
            $Version = $Matches[1]
        } else {
            $Version = "v0.1.0"
        }
    }
    $Version = $Version.Trim()
    if (-not $Version.StartsWith("v")) {
        $Version = "v$Version"
    }

    $distDir = Join-Path $repoRoot "dist"
    if (Test-Path $distDir) {
        Remove-Item -Path $distDir -Recurse -Force
    }
    New-Item -ItemType Directory -Path $distDir -Force | Out-Null

    $stageBase = Join-Path ([System.IO.Path]::GetTempPath()) ("kkpan-rel-" + [System.Guid]::NewGuid().ToString("N"))
    New-Item -ItemType Directory -Path $stageBase -Force | Out-Null

    $checksumLines = New-Object System.Collections.Generic.List[string]
    $targetList = $Targets -split "," | ForEach-Object { $_.Trim() } | Where-Object { $_ -ne "" }

    try {
        foreach ($pair in $targetList) {
            $parts = $pair -split "/"
            if ($parts.Count -ne 2) {
                throw "无效的目标平台格式: $pair (应为 os/arch)"
            }
            $goos = $parts[0].ToLowerInvariant()
            $goarch = $parts[1].ToLowerInvariant()

            $binName = "kkpan"
            if ($goos -eq "windows") {
                $binName = "kkpan.exe"
            }

            $pkgDirName = "kkpan-$Version-$goos-$goarch"
            $pkgDir = Join-Path $stageBase $pkgDirName
            New-Item -ItemType Directory -Path $pkgDir -Force | Out-Null

            Write-Host "==> 正在构建 $pkgDirName ($goos/$goarch)..." -ForegroundColor Cyan

            $prevGOOS = $env:GOOS
            $prevGOARCH = $env:GOARCH
            $prevCGO = $env:CGO_ENABLED
            try {
                $env:GOOS = $goos
                $env:GOARCH = $goarch
                $env:CGO_ENABLED = "0"

                $binOut = Join-Path $pkgDir $binName
                $ldflags = "-s -w -X main.Version=$Version"
                & go build -trimpath -ldflags $ldflags -o $binOut .
                if ($LASTEXITCODE -ne 0) {
                    throw "构建失败: $goos/$goarch"
                }
            } finally {
                $env:GOOS = $prevGOOS
                $env:GOARCH = $prevGOARCH
                $env:CGO_ENABLED = $prevCGO
            }

            foreach ($doc in @("README.md", "LICENSE", "NOTICE")) {
                $srcDoc = Join-Path $repoRoot $doc
                if (Test-Path $srcDoc) {
                    Copy-Item -Path $srcDoc -Destination (Join-Path $pkgDir $doc) -Force
                }
            }

            if ($goos -eq "linux") {
                $archiveName = "$pkgDirName.tar.gz"
                $archivePath = Join-Path $distDir $archiveName
                & tar -czf $archivePath -C $stageBase $pkgDirName
                if ($LASTEXITCODE -ne 0) {
                    throw "打包失败: $archiveName"
                }
            } else {
                $archiveName = "$pkgDirName.zip"
                $archivePath = Join-Path $distDir $archiveName
                Compress-Archive -Path $pkgDir -DestinationPath $archivePath -Force
            }

            $hash = (Get-FileHash -Path $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
            $checksumLines.Add("$hash  $archiveName")
            Write-Host "    [OK] $archiveName (sha256: $($hash.Substring(0, 12))...)" -ForegroundColor Green
        }

        $sumsPath = Join-Path $distDir "SHA256SUMS"
        [System.IO.File]::WriteAllText($sumsPath, ($checksumLines -join "`n") + "`n", [System.Text.UTF8Encoding]::new($false))
        Write-Host ""
        Write-Host "✔ 全部产物已生成至 dist/ 目录，校验文件: $sumsPath" -ForegroundColor Green
    } finally {
        if (Test-Path $stageBase) {
            Remove-Item -Path $stageBase -Recurse -Force -ErrorAction SilentlyContinue
        }
    }
} finally {
    Pop-Location
}
