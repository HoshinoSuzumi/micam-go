#!/bin/sh
# 把当前源码打包成某个架构的 fpk：
#
#   ./packaging/build-fpk.sh amd64     -> micam_<版本>_amd64.fpk  (platform=x86)
#   ./packaging/build-fpk.sh arm64     -> micam_<版本>_arm64.fpk  (platform=arm)
#
# 服务以静态二进制运行，所以包里是 arch 专属的可执行文件，manifest 的 platform
# 必须跟着改成 x86 / arm（官方要求 all 只用于不含架构二进制的包）。
#
# 需要 fnpack：https://developer.fnnas.com/docs/cli/fnpack
set -eu

ARCH="${1:-}"
case "$ARCH" in
amd64) PLATFORM=x86 ;;
arm64) PLATFORM=arm ;;
*)
    echo "用法: $0 amd64|arm64" >&2
    exit 1
    ;;
esac

ROOT=$(cd "$(dirname "$0")/.." && pwd)
SRC="$ROOT/packaging/fnos"
BUILD="$ROOT/.tmp/fpk-$ARCH"

if [ ! -f "$SRC/manifest" ]; then
    echo "找不到 $SRC/manifest" >&2
    exit 1
fi

# 版本来自 tag（发布时来自 MICAM_VERSION），不来自 manifest：那里面是上一次
# 构建写下的值，正是"0.6.2 的包带着 0.5.0 的二进制"的来源。
VERSION="${MICAM_VERSION:-}"
VERSION="${VERSION#v}"
if [ -z "$VERSION" ]; then
    VERSION=$(git -C "$ROOT" describe --tags --abbrev=0 2>/dev/null | sed 's/^v//' || true)
fi
if [ -z "$VERSION" ]; then
    echo "无法确定版本号：设置 MICAM_VERSION，或者先打一个 tag" >&2
    exit 1
fi

command -v fnpack >/dev/null 2>&1 || {
    echo "找不到 fnpack，先安装：https://developer.fnnas.com/docs/cli/fnpack" >&2
    exit 1
}

# 在临时目录里组装，避免把二进制留在源码树里，也方便一个版本出两种架构。
rm -rf "$BUILD"
mkdir -p "$BUILD"
(cd "$SRC" && tar cf - --exclude=bin .) | (cd "$BUILD" && tar xf -)

mkdir -p "$BUILD/app/bin"
CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" \
    go build -trimpath \
    -ldflags="-s -w -X 'micam/internal/cli.Version=${VERSION}'" \
    -o "$BUILD/app/bin/micam" "$ROOT/cmd/micam"

# 界面是构建产物，不进仓库：没有就先构建，别打出一个没有 UI 的包。
WEB="$ROOT/web/dist"
if [ ! -f "$WEB/index.html" ]; then
    echo "还没有构建界面，先跑：make web" >&2
    exit 1
fi
rm -rf "$BUILD/app/www"
mkdir -p "$BUILD/app/www"
cp -R "$WEB/." "$BUILD/app/www/"

# 版本与架构都写进 manifest；sed -i.bak 在 GNU 和 BSD 上都能用。
sed -i.bak "s|^version=.*|version=\"$VERSION\"|" "$BUILD/manifest"
sed -i.bak "s|^platform=.*|platform=\"$PLATFORM\"|" "$BUILD/manifest"
rm -f "$BUILD/manifest.bak"

grep -n '^version=' "$BUILD/manifest"
grep -n '^platform=' "$BUILD/manifest"

echo "building micam $VERSION for $ARCH ($PLATFORM)"

OUT="$ROOT/micam_${VERSION}_${ARCH}.fpk"
rm -f "$ROOT/micam.fpk" "$OUT"

(cd "$ROOT" && fnpack build --directory "$BUILD")

# fnpack 用 manifest.appname 当文件名，统一改成带版本和架构的。
if [ -f "$ROOT/micam.fpk" ]; then
    mv -f "$ROOT/micam.fpk" "$OUT"
fi

if [ ! -f "$OUT" ]; then
    echo "打包失败：没有生成 $OUT" >&2
    exit 1
fi

echo "$OUT"
