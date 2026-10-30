#!/bin/sh
set -eu
umask 077
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"
VERSION=${1:-$(cat VERSION)}
case "$VERSION" in ''|*[!0-9a-zA-Z.-]*) echo '版本包含非法字符' >&2; exit 1 ;; esac
printf '%s\n' "$VERSION" | LC_ALL=C grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9][a-zA-Z0-9.-]*)?$' || { echo '版本必须为 1.2.3 或 1.2.3-rc.1 格式' >&2; exit 1; }
PLATFORM=${AUTH_RELEASE_PLATFORM:-linux/amd64}
case "$PLATFORM" in linux/amd64) ARCH=amd64 ;; linux/arm64) ARCH=arm64 ;; *) echo '仅支持 linux/amd64 和 linux/arm64' >&2; exit 1 ;; esac
OUT="$ROOT/dist/auth-center-$VERSION-$ARCH.tar.gz"
[ ! -e "$OUT" ] || { echo '发布包已存在；请使用新版本号，禁止覆盖已发布的安装包。' >&2; exit 1; }
mkdir -p "$ROOT/dist"
WORK=$(mktemp -d "$ROOT/dist/.release.XXXXXX")
trap 'rm -rf "$WORK"' EXIT HUP INT TERM
BUNDLE="$WORK/auth-center"
mkdir -p "$BUNDLE/packages"
IMAGE="auth-center:$VERSION-$ARCH"
docker build --platform "$PLATFORM" --build-arg "VERSION=$VERSION" -t "$IMAGE" .
docker save -o "$BUNDLE/packages/$VERSION.tar" "$IMAGE"
if command -v sha256sum >/dev/null 2>&1; then HASH=$(sha256sum "$BUNDLE/packages/$VERSION.tar" | awk '{print $1}');
else HASH=$(shasum -a 256 "$BUNDLE/packages/$VERSION.tar" | awk '{print $1}'); fi
ID=$(docker image inspect --format '{{.Id}}' "$IMAGE")
printf '%s\n%s\n1\n' "$ID" "$HASH" > "$BUNDLE/packages/$VERSION.meta"
printf '%s\n' "$VERSION" > "$BUNDLE/RELEASE"
cp authctl.sh compose.yaml .env.docker.example README.md "$BUNDLE/"
chmod 755 "$BUNDLE/authctl.sh"
# macOS resource-fork sidecars are not installation packages and must not ship.
COPYFILE_DISABLE=1 tar -czf "$WORK/release.tar.gz" -C "$WORK" auth-center
mv "$WORK/release.tar.gz" "$OUT"
echo "已生成：${OUT}（${PLATFORM}）。发布包不包含 .env、数据库或主密钥。"
