#!/usr/bin/env bash
# kkpan-cli (kkpan) - Linux & macOS 独立一键安装脚本
# 用法:
#   curl -fsSL https://raw.githubusercontent.com/A-510/kkpan-cli/main/install.sh | bash
#   KKPAN_VERSION=v0.1.0 curl -fsSL https://raw.githubusercontent.com/A-510/kkpan-cli/main/install.sh | bash

set -euo pipefail

REPO="${KKPAN_REPO:-A-510/kkpan-cli}"
BINARY_NAME="kkpan"

RED='\033[0;31m'
GREEN='\033[0;32m'
BLUE='\033[0;34m'
YELLOW='\033[1;33m'
BOLD='\033[1m'
NC='\033[0m'

info() {
    printf "${BLUE}==>${NC} ${BOLD}%s${NC}\n" "$1"
}

success() {
    printf "${GREEN}[OK]${NC} ${BOLD}%s${NC}\n" "$1"
}

warn() {
    printf "${YELLOW}[!]${NC} %s\n" "$1"
}

error() {
    printf "${RED}[x] 错误:${NC} %s\n" "$1" >&2
    exit 1
}

OS="$(uname -s)"
case "$OS" in
    Linux)
        PLATFORM="linux"
        EXT="tar.gz"
        ;;
    Darwin)
        PLATFORM="darwin"
        EXT="zip"
        ;;
    *)
        error "不支持的操作系统: $OS。请前往 https://github.com/$REPO/releases 下载预编译产物。"
        ;;
esac

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64)
        TARGET_ARCH="amd64"
        ;;
    aarch64|arm64)
        TARGET_ARCH="arm64"
        ;;
    *)
        error "不支持的系统架构: $ARCH。"
        ;;
esac

if command -v curl >/dev/null 2>&1; then
    DOWNLOADER="curl"
elif command -v wget >/dev/null 2>&1; then
    DOWNLOADER="wget"
else
    error "需要 curl 或 wget 工具以下载安装包。"
fi

if [ "$EXT" = "zip" ] && ! command -v unzip >/dev/null 2>&1; then
    error "解压 macOS 安装包需要 unzip 命令，请先安装 unzip。"
fi
if [ "$EXT" = "tar.gz" ] && ! command -v tar >/dev/null 2>&1; then
    error "解压 Linux 安装包需要 tar 命令，请先安装 tar。"
fi

if [ -n "${KKPAN_VERSION:-}" ]; then
    VERSION="$KKPAN_VERSION"
    case "$VERSION" in
        v*) ;;
        *) VERSION="v$VERSION" ;;
    esac
else
    info "正在从 GitHub ($REPO) 获取最新版本..."
    TAG=""
    if [ "$DOWNLOADER" = "curl" ]; then
        TAG="$(curl -sI "https://github.com/$REPO/releases/latest" 2>/dev/null | grep -i '^location:' | sed 's/.*tag\///' | tr -d '\r\n' || true)"
        if [ -z "$TAG" ]; then
            TAG="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' | head -n 1 || true)"
        fi
    else
        TAG="$(wget -qO- "https://api.github.com/repos/$REPO/releases/latest" 2>/dev/null | grep '"tag_name":' | sed -E 's/.*"([^"]+)".*/\1/' | head -n 1 || true)"
    fi
    if [ -z "$TAG" ]; then
        error "无法获取最新版本号，可通过环境变量指定：KKPAN_VERSION=v0.1.0 bash install.sh"
    fi
    VERSION="$TAG"
fi

ARCHIVE_DIR="${BINARY_NAME}-${VERSION}-${PLATFORM}-${TARGET_ARCH}"
PKG_NAME="${ARCHIVE_DIR}.${EXT}"
BASE_URL="${KKPAN_RELEASE_BASE_URL:-https://github.com/$REPO/releases/download/${VERSION}}"
DOWNLOAD_URL="${BASE_URL%/}/${PKG_NAME}"
CHECKSUM_URL="${BASE_URL%/}/SHA256SUMS"

info "检测到运行环境: ${PLATFORM}-${TARGET_ARCH}"
info "准备安装 kkpan ${VERSION}..."

TMP_DIR="$(mktemp -d)"
cleanup() {
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

info "正在下载 ${DOWNLOAD_URL}..."
if [ "$DOWNLOADER" = "curl" ]; then
    curl -fL --progress-bar "$DOWNLOAD_URL" -o "$TMP_DIR/$PKG_NAME"
    curl -fsSL "$CHECKSUM_URL" -o "$TMP_DIR/SHA256SUMS" 2>/dev/null || true
else
    wget -q --show-progress -O "$TMP_DIR/$PKG_NAME" "$DOWNLOAD_URL"
    wget -qO "$TMP_DIR/SHA256SUMS" "$CHECKSUM_URL" 2>/dev/null || true
fi

if [ -s "$TMP_DIR/SHA256SUMS" ]; then
    EXPECTED="$(grep -E "[[:space:]]\*?${PKG_NAME}$" "$TMP_DIR/SHA256SUMS" | awk '{print $1}' | head -n 1 || true)"
    if [ -n "$EXPECTED" ]; then
        ACTUAL=""
        if command -v sha256sum >/dev/null 2>&1; then
            ACTUAL="$(sha256sum "$TMP_DIR/$PKG_NAME" | awk '{print $1}')"
        elif command -v shasum >/dev/null 2>&1; then
            ACTUAL="$(shasum -a 256 "$TMP_DIR/$PKG_NAME" | awk '{print $1}')"
        fi
        if [ -n "$ACTUAL" ] && [ "$ACTUAL" != "$EXPECTED" ]; then
            error "SHA256 校验失败: $PKG_NAME 期望 $EXPECTED，实际 $ACTUAL"
        fi
        if [ -n "$ACTUAL" ]; then
            success "SHA256 校验通过 ($PKG_NAME)"
        fi
    fi
fi

info "正在解压安装包..."
if [ "$EXT" = "zip" ]; then
    unzip -q "$TMP_DIR/$PKG_NAME" -d "$TMP_DIR"
else
    tar -xzf "$TMP_DIR/$PKG_NAME" -C "$TMP_DIR"
fi

BIN_PATH=""
if [ -f "$TMP_DIR/$ARCHIVE_DIR/$BINARY_NAME" ]; then
    BIN_PATH="$TMP_DIR/$ARCHIVE_DIR/$BINARY_NAME"
elif [ -f "$TMP_DIR/$BINARY_NAME" ]; then
    BIN_PATH="$TMP_DIR/$BINARY_NAME"
else
    BIN_PATH="$(find "$TMP_DIR" -maxdepth 3 -type f -name "$BINARY_NAME" | head -n 1 || true)"
fi

if [ -z "$BIN_PATH" ] || [ ! -f "$BIN_PATH" ]; then
    error "在安装包中未找到可执行文件 '$BINARY_NAME'。"
fi

if [ -n "${KKPAN_INSTALL_DIR:-}" ]; then
    INSTALL_DIR="$KKPAN_INSTALL_DIR"
elif [ -w "/usr/local/bin" ]; then
    INSTALL_DIR="/usr/local/bin"
else
    INSTALL_DIR="${HOME}/.local/bin"
fi

mkdir -p "$INSTALL_DIR"
install -m 755 "$BIN_PATH" "$INSTALL_DIR/$BINARY_NAME"

success "kkpan ${VERSION} 已成功安装到: ${INSTALL_DIR}/${BINARY_NAME}"

case ":$PATH:" in
    *":$INSTALL_DIR:"*) ;;
    *)
        echo ""
        warn "${INSTALL_DIR} 尚未加入当前 PATH。"
        echo "可执行以下命令将 ${INSTALL_DIR} 加入 shell PATH："
        echo "  export PATH=\"${INSTALL_DIR}:\$PATH\""
        ;;
esac

echo "执行 '${BINARY_NAME} --help' 或 '${BINARY_NAME} ui' 即可开始使用！"
