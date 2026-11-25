@echo off
setlocal enabledelayedexpansion

if defined KKPAN_BIN (
  if exist "%KKPAN_BIN%" (
    "%KKPAN_BIN%" %*
    exit /b %ERRORLEVEL%
  )
)

where kkpan >nul 2>&1
if %ERRORLEVEL%==0 (
  kkpan %*
  exit /b %ERRORLEVEL%
)

set "SCRIPT_DIR=%~dp0"
set "REPO_BIN=%SCRIPT_DIR%..\..\..\..\kkpan.exe"
if exist "%REPO_BIN%" (
  "%REPO_BIN%" %*
  exit /b %ERRORLEVEL%
)

set "CACHE_DIR=%LOCALAPPDATA%\kkpan-cli\bin"
set "CACHED_BIN=%CACHE_DIR%\kkpan.exe"
if exist "%CACHED_BIN%" (
  "%CACHED_BIN%" %*
  exit /b %ERRORLEVEL%
)

powershell -NoProfile -ExecutionPolicy Bypass -Command ^
  "$ErrorActionPreference='Stop';" ^
  "try { [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12 } catch {};" ^
  "if (-not [Environment]::Is64BitOperatingSystem) { throw 'Only 64-bit (amd64 / arm64) Windows is supported.' };" ^
  "$repo = if ($env:KKPAN_REPO) { $env:KKPAN_REPO } else { 'A-510/kkpan-cli' };" ^
  "$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64' -or $env:PROCESSOR_ARCHITEW6432 -eq 'ARM64') { 'arm64' } else { 'amd64' };" ^
  "$tag = $null;" ^
  "try {" ^
  "  $req = [Net.WebRequest]::Create('https://github.com/' + $repo + '/releases/latest');" ^
  "  $req.AllowAutoRedirect = $false;" ^
  "  $resp = $req.GetResponse();" ^
  "  $loc = $resp.GetResponseHeader('Location');" ^
  "  $resp.Close();" ^
  "  if ($loc -match '/tag/([^/]+)$') { $tag = $Matches[1] }" ^
  "} catch {};" ^
  "if (-not $tag) {" ^
  "  $rel = Invoke-RestMethod -Uri ('https://api.github.com/repos/' + $repo + '/releases/latest') -Headers @{ 'User-Agent' = 'kkpan-plugin' };" ^
  "  $tag = $rel.tag_name" ^
  "};" ^
  "if (-not $tag) { throw 'Unable to resolve latest release tag.' };" ^
  "$dlUrl = 'https://github.com/' + $repo + '/releases/download/' + $tag + '/kkpan-' + $tag + '-windows-' + $arch + '.zip';" ^
  "$tmp = Join-Path $env:TEMP ('kkpan-' + [guid]::NewGuid().ToString('N'));" ^
  "New-Item -ItemType Directory -Force -Path $tmp | Out-Null;" ^
  "try {" ^
  "  $zip = Join-Path $tmp 'kkpan.zip';" ^
  "  Invoke-WebRequest -Uri $dlUrl -OutFile $zip -UseBasicParsing;" ^
  "  Expand-Archive -Path $zip -DestinationPath $tmp -Force;" ^
  "  $exe = Get-ChildItem -Path $tmp -Filter 'kkpan.exe' -Recurse -File | Select-Object -First 1;" ^
  "  if (-not $exe) { throw 'kkpan.exe not found inside archive.' };" ^
  "  New-Item -ItemType Directory -Force -Path '%CACHE_DIR%' | Out-Null;" ^
  "  Copy-Item -Path $exe.FullName -Destination '%CACHED_BIN%' -Force;" ^
  "} finally { Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue }"

if exist "%CACHED_BIN%" (
  "%CACHED_BIN%" %*
  exit /b %ERRORLEVEL%
)

echo Error: kkpan.exe not found and automatic download failed. 1>&2
echo Install via: irm https://raw.githubusercontent.com/A-510/kkpan-cli/main/install.ps1 ^| iex (or: npm i -g kkpan-cli / bun add -g kkpan-cli) 1>&2
exit /b 127
