#!/bin/sh
# 构建镜像时预装 mysqldump / mysql 客户端（docs/spec.md §13）。
#
# 用法：fetch-mysql-tools.sh <manifest.json> <平台，例如 linux-amd64> <安装根目录> [下载地址前缀]
#   读取 lemi-service/internal/dumptools/manifest.json：
#     {"versions":[{"version":"8.4.7","series":"8.4","tag":"tools-mysql-8.4.7",
#                   "files":{"linux-amd64":{"name":"...tar.gz","sha256":"..."}, ...}}]}
#   逐个版本从 <前缀>/<tag>/<name> 下载，校验 SHA-256，解压到 <安装根目录>/mysql-<版本>/<平台>/。
#   清单不存在、为空或没有当前平台的文件时跳过（镜像照常构建，运行时由 Lemi 按需下载到数据目录）。
#   SHA-256 不符时构建失败。
set -eu

MANIFEST="$1"
PLAT="$2"
DEST="$3"
BASE="${4:-https://github.com/LemonITCN/lemi/releases/download}"

mkdir -p "$DEST"
if [ ! -s "$MANIFEST" ]; then
  echo "没有 mysqldump 工具清单，跳过预装"
  exit 0
fi
LIST="$(jq -r --arg p "$PLAT" '(.versions // [])[] | select(.files[$p] != null)
  | [.version, .tag, .files[$p].name, .files[$p].sha256] | @tsv' "$MANIFEST")"
if [ -z "$LIST" ]; then
  echo "清单里没有 ${PLAT} 的 mysqldump 工具，跳过预装"
  exit 0
fi

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
printf '%s\n' "$LIST" | while IFS="$(printf '\t')" read -r ver tag name sum; do
  case "$name" in */*|..*|'') echo "清单里的文件名不合法：${name}" >&2; exit 1 ;; esac
  echo "预装 mysql-${ver}（${PLAT}）：${name}"
  curl -fsSL --retry 5 --retry-delay 3 -o "$TMP/$name" "$BASE/$tag/$name"
  echo "$sum  $TMP/$name" | sha256sum -c -
  out="$DEST/mysql-$ver/$PLAT"
  rm -rf "$out" "$TMP/x"
  mkdir -p "$out" "$TMP/x"
  case "$name" in
    *.tar.gz|*.tgz) tar -xzf "$TMP/$name" -C "$TMP/x" ;;
    *.tar.xz) tar -xJf "$TMP/$name" -C "$TMP/x" ;;
    *) echo "不支持的归档格式：${name}" >&2; exit 1 ;;
  esac
  # 归档里只有一个顶层目录时去掉这一层，保证 <安装根目录>/mysql-<版本>/<平台>/ 下直接是工具文件
  src="$TMP/x"
  if [ "$(find "$src" -mindepth 1 -maxdepth 1 | wc -l)" -eq 1 ] && [ -d "$(find "$src" -mindepth 1 -maxdepth 1)" ]; then
    src="$(find "$src" -mindepth 1 -maxdepth 1)"
  fi
  cp -a "$src/." "$out/"
  rm -f "$TMP/$name"
done
find "$DEST" -maxdepth 3 -mindepth 1 -type d | sort
