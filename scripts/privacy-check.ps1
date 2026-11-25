# 发布前隐私与敏感凭据安全自检脚本
# 在执行 git push / npm publish / bun publish / 打包发布前运行，确保绝不泄露用户夸克账号 Cookie 或本机个人隐私路径。
param(
    [switch]$SkipNpmPack
)

$ErrorActionPreference = "Stop"

$repoRoot = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$violations = New-Object System.Collections.Generic.List[string]

Write-Host "==> 正在执行 kkpan-cli 发布前隐私与凭据安全自检..." -ForegroundColor Cyan

# 1. 检查高危敏感文件名是否误留在仓库目录内
$forbiddenFileNames = @(
    "kkpan_config.json",
    "kkpan_config.json.tmp",
    "kkpan_history.txt",
    "qrcode.png",
    ".env"
)
$forbiddenPatterns = @(
    "*.kkpan.state.json",
    "*.kkpan.upload.json",
    "*.kkpan.downloading",
    ".env.*",
    "*.pem",
    "*.key"
)
$skipDirNames = @(".git", "node_modules", "dist", "out", "vendor", "夸克网盘的cli版本")

Get-ChildItem -Path $repoRoot -Recurse -Force -File | ForEach-Object {
    $full = $_.FullName
    $rel = $full.Substring($repoRoot.Length).TrimStart('\', '/')
    $segments = $rel -split '[\\/]'
    foreach ($seg in $segments) {
        if ($skipDirNames -contains $seg) {
            return
        }
    }

    $name = $_.Name
    if ($forbiddenFileNames -contains $name) {
        $violations.Add("发现敏感配置文件或会话产物: $rel")
        return
    }
    foreach ($pat in $forbiddenPatterns) {
        if ($name -like $pat) {
            $violations.Add("发现敏感临时/密钥文件 ($pat): $rel")
            return
        }
    }
}

# 2. 扫描源码、脚本与插件文件正文，拦截真实夸克 Cookie 与本机真实用户路径
$scanExts = @(".go", ".ps1", ".sh", ".cmd", ".mjs", ".js", ".ts", ".json", ".md", ".yml", ".yaml", ".toml")
$cookieRegex = '(?:__puus|__pus)\s*=\s*[A-Za-z0-9%+/=_-]{24,}'
$localUserPathRegex = '(?i)C:\\Users\\([A-Za-z0-9._-]+)'
$allowedPlaceholderUsers = @("yourname", "user", "username", "example", "alice", "bob")

Get-ChildItem -Path $repoRoot -Recurse -Force -File | ForEach-Object {
    $full = $_.FullName
    $rel = $full.Substring($repoRoot.Length).TrimStart('\', '/')
    $segments = $rel -split '[\\/]'
    foreach ($seg in $segments) {
        if ($skipDirNames -contains $seg) {
            return
        }
    }
    if (-not ($scanExts -contains $_.Extension.ToLowerInvariant())) {
        return
    }
    # 自检脚本自身包含检测说明正则，跳过以免误报
    if ($rel -eq "scripts\privacy-check.ps1" -or $rel -eq "scripts/privacy-check.ps1") {
        return
    }

    $content = Get-Content -Path $full -Raw -Encoding UTF8 -ErrorAction SilentlyContinue
    if (-not $content) {
        return
    }

    if ($content -match $cookieRegex) {
        $violations.Add("疑似包含真实夸克登录 Cookie (__pus/__puus 长串): $rel")
    }

    # 非单测文件禁止硬编码本机真实 C:\Users\<用户名> 路径
    if (-not $_.Name.EndsWith("_test.go")) {
        $matches = [regex]::Matches($content, $localUserPathRegex)
        foreach ($m in $matches) {
            $uname = $m.Groups[1].Value.ToLowerInvariant()
            if ($allowedPlaceholderUsers -notcontains $uname) {
                $violations.Add("疑似硬编码本机个人路径 ($($m.Value)): $rel")
            }
        }
    }
}

# 3. 校验 npm / bun 打包白名单（确保 package.json 不会打包源码或敏感文件）
if (-not $SkipNpmPack) {
    $npmCmd = Get-Command npm -ErrorAction SilentlyContinue
    if ($npmCmd) {
        Write-Host "==> 正在校验 npm pack --dry-run 分发清单..." -ForegroundColor Cyan
        Push-Location $repoRoot
        try {
            $packRaw = & npm pack --dry-run --json 2>$null
            if ($LASTEXITCODE -eq 0 -and $packRaw) {
                $packInfo = ($packRaw -join "`n") | ConvertFrom-Json
                if ($packInfo -is [System.Array] -and $packInfo.Count -gt 0) {
                    $packInfo = $packInfo[0]
                }
                $allowedPrefixes = @("bin/", "scripts/platform.mjs", "scripts/postinstall.mjs", "package.json", "README.md", "LICENSE", "NOTICE", "DESIGN.md")
                foreach ($f in $packInfo.files) {
                    $p = ($f.path -replace '\\', '/')
                    $ok = $false
                    foreach ($prefix in $allowedPrefixes) {
                        if ($p -eq $prefix -or ($prefix.EndsWith("/") -and $p.StartsWith($prefix))) {
                            $ok = $true
                            break
                        }
                    }
                    if (-not $ok) {
                        $violations.Add("npm/bun 待打包文件超出安全白名单: $p")
                    }
                    Write-Host "    [pack] $p ($($f.size) B)" -ForegroundColor DarkGray
                }
            } else {
                $violations.Add("npm pack --dry-run 执行失败，请检查 package.json")
            }
        } finally {
            Pop-Location
        }
    } else {
        Write-Host "[!] 未检测到 npm 命令，已跳过 npm pack 白名单预览。" -ForegroundColor Yellow
    }
}

if ($violations.Count -gt 0) {
    Write-Host ""
    Write-Host "✖ 隐私安全自检未通过，已拦截以下风险项：" -ForegroundColor Red
    foreach ($v in $violations) {
        Write-Host "  - $v" -ForegroundColor Red
    }
    exit 1
}

Write-Host "✔ 隐私安全自检通过：未检测到夸克账号 Cookie、本机隐私路径或非白名单打包文件。" -ForegroundColor Green
exit 0
