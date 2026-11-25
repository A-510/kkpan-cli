#!/usr/bin/env bash
# kkpan-cli 多平台交叉编译与打包脚本 (Linux / macOS)
# 用法:
#   ./scripts/release.sh v0.1.0

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT_DIR"

VERSION="${1:-}"
if [ -z "$VERSION" ]; then
  VERSION="$(sed -n 's/.*Version[[:space:]]*=[[:space:]]*"\([^"]*\)".*/\1/p' main.go | head -n 1)"
fi
[ -n "$VERSION" ] || VERSION="v0.1.0"
case "$VERSION" in
  v*) ;;
  *) VERSION="v$VERSION" ;;
esac

DIST_DIR="$ROOT_DIR/dist"
rm -rf "$DIST_DIR"
mkdir -p "$DIST_DIR"

STAGE_DIR="$(mktemp -d)"
trap 'rm -rf "$STAGE_DIR"' EXIT

build_one() {
  local os="$1"
  local arch="$2"
  local pkg="kkpan-${VERSION}-${os}-${arch}"
  local dir="$STAGE_DIR/$pkg"
  local bin="kkpan"
  [ "$os" = "windows" ] && bin="kkpan.exe"

  mkdir -p "$dir"
  echo "==> 正在构建 $pkg ($os/$arch)..."
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "-s -w -X main.Version=${VERSION}" -o "$dir/$bin" .

  for f in README.md LICENSE NOTICE; do
    [ -f "$ROOT_DIR/$f" ] && cp "$ROOT_DIR/$f" "$dir/$f"
  done

  if [ "$os" = "linux" ]; then
    tar -czf "$DIST_DIR/$pkg.tar.gz" -C "$STAGE_DIR" "$pkg"
  else
    (cd "$STAGE_DIR" && zip -qr "$DIST_DIR/$pkg.zip" "$pkg")
  fi
}

build_one windows amd64
build_one windows arm64
build_one darwin amd64
build_one darwin arm64
build_one linux amd64
build_one linux arm64

(
  cd "$DIST_DIR"
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum kkpan-* > SHA256SUMS
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 kkpan-* > SHA256SUMS
  fi
)

echo "✔ 构建完成，全部压缩包与 SHA256SUMS 已输出至 $DIST_DIR"
