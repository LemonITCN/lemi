#!/usr/bin/env bash
# 本机一键构建：前端 + 全部平台的 lemi 可执行文件与发布包，输出到 dist/（CI 也用它，产物命名与 Release 一致）。
#
# 用法：scripts/build.sh [选项] [目标 ...]
#   目标：linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64 windows/arm64，
#         也可以只写系统名（linux、darwin、windows）；不写就是全部六个
# 选项：
#   --skip-console   不构建前端，直接用 lemi-service/internal/web/dist 里已有的产物
#   --no-package     只编译到 <输出目录>/bin/<系统>-<架构>/，不打包
#   --package-only   不编译，把 <输出目录>/bin 下已有的可执行文件打包（CI 在 macOS 签名之后用）
# 环境变量：
#   LEMI_VERSION         版本号（默认：HEAD 正好是 v* 标签时取标签，否则取 internal/version 里的值）
#   OUT                  输出目录（默认 <仓库>/dist，已在 .gitignore）
#   MACOS_SIGN_IDENTITY  在 Mac 上编 darwin 目标时的签名身份（例如 "Lemi Code Signing"）；不设则临时签名
#   npm_config_registry  npm 源（npm 自己读这个变量）。国内网络：npm_config_registry=https://registry.npmmirror.com scripts/build.sh
#   GOPROXY              Go 模块代理。国内网络：GOPROXY=https://goproxy.cn,direct
#
# 产物：
#   <输出目录>/bin/<系统>-<架构>/lemi(.exe)
#   <输出目录>/lemi-<版本>-<系统>-<架构>.tar.gz（Windows 为 .zip），里面是 lemi(.exe)、README.md、LICENSE
#   <输出目录>/SHA256SUMS
# 纯 Go、关闭 CGO，前端产物嵌进可执行文件；Windows 两个架构都支持 Windows 10 / 11（ARM64 是原生程序）。
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
SERVICE="$ROOT/lemi-service"
CONSOLE="$ROOT/lemi-console"
MODULE="github.com/LemonITCN/lemi/lemi-service"
ALL=(linux/amd64 linux/arm64 darwin/arm64 darwin/amd64 windows/amd64 windows/arm64)

SKIP_CONSOLE=0
PACKAGE=1
COMPILE=1
WANT=()
for a in "$@"; do
  case "$a" in
    --skip-console) SKIP_CONSOLE=1 ;;
    --no-package) PACKAGE=0 ;;
    --package-only) COMPILE=0 SKIP_CONSOLE=1 ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    -*) echo "未知选项：${a}" >&2; exit 2 ;;
    *) WANT+=("$a") ;;
  esac
done

TARGETS=()
if [ ${#WANT[@]} -eq 0 ]; then
  TARGETS=("${ALL[@]}")
else
  for w in "${WANT[@]}"; do
    found=0
    for t in "${ALL[@]}"; do
      if [ "$t" = "$w" ] || [ "${t%%/*}" = "$w" ]; then TARGETS+=("$t"); found=1; fi
    done
    [ $found = 1 ] || { echo "未知目标：${w}（可选：${ALL[*]}）" >&2; exit 2; }
  done
fi

# ---------- 版本 ----------
V="${LEMI_VERSION:-}"
if [ -z "$V" ] && git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1; then
  TAG="$(git -C "$ROOT" describe --tags --exact-match --match 'v*' HEAD 2>/dev/null || true)"
  [ -n "$TAG" ] && V="${TAG#v}"
fi
if [ -z "$V" ]; then
  V="$(sed -n 's/^[[:space:]]*Version[[:space:]]*=[[:space:]]*"\(.*\)"/\1/p' "$SERVICE/internal/version/version.go" | head -1)"
fi
V="${V#v}"
[ -n "$V" ] || { echo "无法确定版本号" >&2; exit 1; }
COMMIT="${GITHUB_SHA:-}"
DATE=""
if git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1; then
  [ -n "$COMMIT" ] || COMMIT="$(git -C "$ROOT" rev-parse HEAD)"
  DATE="$(TZ=UTC git -C "$ROOT" show -s --format=%cd --date=format-local:%Y-%m-%dT%H:%M:%SZ HEAD 2>/dev/null || true)"
fi
COMMIT="${COMMIT:0:12}"
[ -n "$DATE" ] || DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
LDFLAGS="-s -w -X ${MODULE}/internal/version.Version=${V} -X ${MODULE}/internal/version.Commit=${COMMIT} -X ${MODULE}/internal/version.Date=${DATE}"

OUT="${OUT:-$ROOT/dist}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
echo "Lemi ${V}（${COMMIT:-无提交信息}），输出到 ${OUT}"

# ---------- 前端 ----------
if [ $SKIP_CONSOLE = 0 ]; then
  if [ -f "$CONSOLE/package.json" ]; then
    echo "构建前端 …"
    (cd "$CONSOLE" && npm ci --no-audit --no-fund && npm run build)
    find "$SERVICE/internal/web/dist" -mindepth 1 ! -name .keep -exec rm -rf {} +
    cp -R "$CONSOLE/dist/." "$SERVICE/internal/web/dist/"
  else
    echo "提示：没有 lemi-console/package.json，跳过前端（程序里只有「前端尚未构建」的提示页）" >&2
  fi
fi
if [ $COMPILE = 1 ] && [ ! -f "$SERVICE/internal/web/dist/index.html" ]; then
  echo "提示：lemi-service/internal/web/dist 里没有 index.html，编出来的程序只有占位页" >&2
fi

# ---------- 编译 ----------
sign_macos() {
  [ "$(uname -s)" = Darwin ] || return 0
  local id="${MACOS_SIGN_IDENTITY:--}"
  codesign --force --options runtime --identifier cn.lemonit.lemi --sign "$id" "$1"
  codesign --verify --verbose=1 "$1"
}

if [ $COMPILE = 1 ]; then
  for t in "${TARGETS[@]}"; do
    goos="${t%%/*}" goarch="${t##*/}"
    bin="lemi"
    [ "$goos" = windows ] && bin="lemi.exe"
    dir="$OUT/bin/$goos-$goarch"
    mkdir -p "$dir"
    echo "  编译 ${goos}/${goarch}"
    (cd "$SERVICE" && CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" \
      go build -trimpath -ldflags "$LDFLAGS" -o "$dir/$bin" ./cmd/lemi)
    [ "$goos" = darwin ] && sign_macos "$dir/$bin"
  done
fi

[ $PACKAGE = 1 ] || { echo "完成（未打包）"; exit 0; }

# ---------- 打包 ----------
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
TAR_OPTS=()
if tar --version 2>/dev/null | grep -q 'GNU tar'; then
  TAR_OPTS=(--owner=0 --group=0 --numeric-owner)
else
  # bsdtar（macOS）：不带 ._ 附加文件与扩展属性
  export COPYFILE_DISABLE=1
  TAR_OPTS=(--uid 0 --gid 0 --uname root --gname root --no-mac-metadata)
fi
README="$ROOT/README.md"
[ -f "$README" ] || { echo "缺少 README.md" >&2; exit 1; }

for t in "${TARGETS[@]}"; do
  goos="${t%%/*}" goarch="${t##*/}"
  bin="lemi"
  [ "$goos" = windows ] && bin="lemi.exe"
  src="$OUT/bin/$goos-$goarch/$bin"
  [ -f "$src" ] || { echo "缺少 ${src}（先编译）" >&2; exit 1; }
  name="lemi-$V-$goos-$goarch"
  stage="$WORK/$name"
  mkdir -p "$stage"
  cp "$src" "$stage/$bin"
  cp "$README" "$stage/README.md"
  cp "$ROOT/LICENSE" "$stage/LICENSE"
  if [ "$goos" = windows ]; then
    rm -f "$OUT/$name.zip"
    (cd "$stage" && zip -q -X "$OUT/$name.zip" "$bin" README.md LICENSE)
    echo "  打包 ${name}.zip"
  else
    chmod 755 "$stage/$bin"
    chmod 644 "$stage/README.md" "$stage/LICENSE"
    # bsdtar 先处理 -C 再打开归档，归档一律用绝对路径
    tar "${TAR_OPTS[@]}" -C "$stage" -czf "$OUT/$name.tar.gz" "$bin" README.md LICENSE
    echo "  打包 ${name}.tar.gz"
  fi
done

(
  cd "$OUT"
  shopt -s nullglob
  files=(lemi-"$V"-*.tar.gz lemi-"$V"-*.zip)
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "${files[@]}" > SHA256SUMS
  else
    shasum -a 256 "${files[@]}" > SHA256SUMS
  fi
)
echo "完成："
ls -l "$OUT"
cat "$OUT/SHA256SUMS"
