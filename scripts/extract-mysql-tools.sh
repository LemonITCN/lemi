#!/usr/bin/env bash
# 从 MySQL 官方发行包里只取 mysqldump、mysql 及其依赖的动态库，打成 Lemi 用的客户端工具包（docs/spec.md §13）。
#
# 用法：
#   scripts/extract-mysql-tools.sh <官方归档文件> <平台> <版本> <输出目录>
#     平台：linux-amd64 | linux-arm64 | darwin-arm64 | darwin-amd64 | windows-amd64
#     版本：如 8.4.7、8.4.13.1，必须与官方归档一致（自检时核对 --version 输出）
#   官方归档必须事先校验过（CI 里由 .github/workflows/mysql-tools.yml 校验 GPG 签名或官方 MD5）。
#
# 产物（写到输出目录，自检通过后才写）：
#   mysql-client-<版本>-<平台>.tar.gz（Windows 为 .zip）
#   manifest-<版本>-<平台>.json   清单片段，CI 汇总成 lemi-service/internal/dumptools/manifest.json
#
# 包内布局（Go 端按此解压与查找，不能改）：没有顶层目录，
#   bin/mysqldump、bin/mysql          Windows 为 bin/mysqldump.exe、bin/mysql.exe，所需 DLL 也放 bin/
#   lib/<依赖库>                       仅 Linux、macOS
#   LICENSE                           原样取自官方发行包
#   README-lemi.txt                   中英文说明：来源、许可证、对应源码地址、提取了哪些文件、做了哪些改动
#   包内没有符号链接。
#
# 依赖库收集规则（不手写库清单）：
#   Linux：  ldd 给出的传递依赖中，解析到官方归档目录内的库全部打包；glibc 基础库（libc、libm、libpthread、
#            libdl、librt、ld-linux 等）不打包；解析到归档外的其他系统库（libstdc++、libgcc_s、libncurses、
#            libtinfo 等）也不打包，只在日志与 README 里列出——构建机（Ubuntu 24.04）上的这些库要求 glibc 2.33~2.38，
#            打进包里反而让官方 glibc 2.28/2.17 基线的程序在旧系统上跑不起来。
#            程序 RUNPATH 改为 $ORIGIN/../lib，依赖同目录库的库 RUNPATH 改为 $ORIGIN。
#   macOS：  otool -L 递归解析 @rpath / @loader_path / @executable_path；/usr/lib 与 /System 下的系统库不打包；
#            程序依赖改为 @rpath/<库> 并只留 rpath @loader_path/../lib，库之间改为 @loader_path/<库>，
#            改完后对所有改过的文件重新 ad-hoc 签名（codesign -f -s -），否则 arm64 上会被系统杀掉。
#   Windows：读 PE 导入表（含延迟导入），在官方 bin/（其次 lib/）里找得到的 DLL 递归打包进 bin/，
#            找不到的视为系统 DLL（含 VC++ 运行库），只在日志与 README 里列出。
#
# 可选环境变量：
#   MYSQL_ARCHIVE_URL    官方归档的下载地址，写进 README-lemi.txt
#   MYSQL_VERIFY_METHOD  官方归档的校验方式说明，写进 README-lemi.txt
#   KEEP_WORK=1          保留中间目录，便于排查
#
# 所需工具：Linux：ldd、patchelf、strip；macOS：otool、install_name_tool、codesign；Windows（Git Bash）：python。
# 兼容 macOS 自带的 bash 3.2：不用关联数组、mapfile，不展开可能为空的数组。

set -euo pipefail

log() { printf '[提取] %s\n' "$*" >&2; }
warn() {
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
		printf '::warning::%s\n' "$*" >&2
	else
		printf '[提取] 警告：%s\n' "$*" >&2
	fi
}
die() {
	if [ "${GITHUB_ACTIONS:-}" = "true" ]; then
		printf '::error::%s\n' "$*" >&2
	else
		printf '[提取] 错误：%s\n' "$*" >&2
	fi
	exit 1
}

usage() {
	sed -n '2,8p' "$0" >&2
	exit 2
}

[ "$#" -eq 4 ] || usage

ARCHIVE_IN=$1
PLATFORM=$2
VERSION=$3
OUT_IN=$4

# ---------- 参数检查 ----------

[ -f "${ARCHIVE_IN}" ] || die "官方归档不存在：${ARCHIVE_IN}"
printf '%s' "${VERSION}" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+(\.[0-9]+)?$' || die "版本号格式不对：${VERSION}"
SERIES=$(printf '%s' "${VERSION}" | cut -d. -f1,2)
TAG="tools-mysql-${VERSION}"

case "${PLATFORM}" in
linux-amd64 | linux-arm64)
	OS=linux
	EXE=""
	PKG_EXT=tar.gz
	;;
darwin-arm64 | darwin-amd64)
	OS=darwin
	EXE=""
	PKG_EXT=tar.gz
	;;
windows-amd64)
	OS=windows
	EXE=".exe"
	PKG_EXT=zip
	;;
*) die "不支持的平台：${PLATFORM}（可选 linux-amd64、linux-arm64、darwin-arm64、darwin-amd64、windows-amd64）" ;;
esac

case "$(uname -s)" in
Linux) HOST=linux ;;
Darwin) HOST=darwin ;;
MINGW* | MSYS* | CYGWIN*) HOST=windows ;;
*) HOST=unknown ;;
esac
[ "${HOST}" = "${OS}" ] || die "平台 ${PLATFORM} 必须在对应系统上提取（需要运行程序自检），当前系统：$(uname -s)"

ARCHIVE_NAME=$(basename "${ARCHIVE_IN}")
ARCHIVE="$(cd "$(dirname "${ARCHIVE_IN}")" && pwd -P)/${ARCHIVE_NAME}"
mkdir -p "${OUT_IN}"
OUT=$(cd "${OUT_IN}" && pwd -P)

PKG_NAME="mysql-client-${VERSION}-${PLATFORM}.${PKG_EXT}"
MANIFEST_NAME="manifest-${VERSION}-${PLATFORM}.json"
SOURCE_URL="https://cdn.mysql.com/Downloads/MySQL-${SERIES}/mysql-${VERSION}.tar.gz"
SOURCE_ARCHIVE_URL="https://cdn.mysql.com/archives/mysql-${SERIES}/mysql-${VERSION}.tar.gz"

# ---------- 工作目录 ----------

WORK=$(mktemp -d "${TMPDIR:-/tmp}/lemi-mysql-tools.XXXXXX")
WORK=$(cd "${WORK}" && pwd -P)
cleanup() {
	rc=$?
	if [ "${KEEP_WORK:-0}" = "1" ]; then
		log "保留中间目录：${WORK}"
	elif [ -n "${WORK:-}" ] && [ -d "${WORK}" ]; then
		chmod -R u+w "${WORK}" 2>/dev/null || true
		rm -rf "${WORK}"
	fi
	exit "${rc}"
}
trap cleanup EXIT

SRC="${WORK}/src"
STAGE="${WORK}/stage"
mkdir -p "${SRC}" "${STAGE}/bin"
# 包内的目录：Windows 没有 lib/
if [ "${OS}" = "windows" ]; then
	PKG_DIRS="bin"
else
	PKG_DIRS="bin lib"
	mkdir -p "${STAGE}/lib"
fi
: >"${WORK}/syslibs.txt"

# ---------- 通用工具函数 ----------

# 可用的 python（Windows 上解压/打 zip、读 PE 导入表用）；跳过 Windows 应用商店的占位程序
PY=""
find_python() {
	local c
	for c in python3 python; do
		if command -v "${c}" >/dev/null 2>&1 && "${c}" -c 'import sys; sys.exit(0)' >/dev/null 2>&1; then
			PY=$(command -v "${c}")
			return 0
		fi
	done
	return 1
}

sha256_of() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{print $1}'
	else
		shasum -a 256 "$1" | awk '{print $1}'
	fi
}

realpath_of() {
	if command -v realpath >/dev/null 2>&1; then
		realpath "$1"
	else
		readlink -f "$1"
	fi
}

lower() { printf '%s' "$1" | tr '[:upper:]' '[:lower:]'; }

# 在文件里查一行是否存在（整行精确匹配）
has_line() { [ -f "$2" ] && grep -qxF -- "$1" "$2"; }

py_unzip() {
	"${PY}" -I - "$1" "$2" <<'PY'
import sys, zipfile
with zipfile.ZipFile(sys.argv[1]) as z:
    z.extractall(sys.argv[2])
PY
}

# 在当前目录按清单打 zip：固定时间戳与顺序，保证同样的输入得到同样的包
py_zip() {
	"${PY}" -I - "$1" "$2" <<'PY'
import os, sys, zipfile
out, lst = sys.argv[1], sys.argv[2]
names = [l.rstrip("\r\n") for l in open(lst, encoding="utf-8") if l.strip()]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
    for n in names:
        if os.path.isdir(n):
            zi = zipfile.ZipInfo(n.rstrip("/") + "/", date_time=(2020, 1, 1, 0, 0, 0))
            zi.create_system = 3
            zi.external_attr = (0o40755 << 16) | 0x10
            z.writestr(zi, b"")
        else:
            zi = zipfile.ZipInfo(n, date_time=(2020, 1, 1, 0, 0, 0))
            zi.create_system = 3
            zi.external_attr = (0o100755 if n.startswith("bin/") else 0o100644) << 16
            zi.compress_type = zipfile.ZIP_DEFLATED
            with open(n, "rb") as f:
                z.writestr(zi, f.read(), compresslevel=9)
PY
}

# 打印 PE 文件导入的 DLL 名（普通导入 + 延迟导入），每行一个
pe_imports() {
	"${PY}" -I - "$1" <<'PY'
import struct, sys
d = open(sys.argv[1], "rb").read()
def u16(o): return struct.unpack_from("<H", d, o)[0]
def u32(o): return struct.unpack_from("<I", d, o)[0]
if d[:2] != b"MZ":
    sys.exit("不是 PE 文件")
pe = u32(0x3C)
if d[pe:pe + 4] != b"PE\0\0":
    sys.exit("不是 PE 文件")
coff = pe + 4
nsec, optsz = u16(coff + 2), u16(coff + 16)
opt = coff + 20
magic = u16(opt)
if magic == 0x20B:
    dd, base = opt + 112, struct.unpack_from("<Q", d, opt + 24)[0]
elif magic == 0x10B:
    dd, base = opt + 96, u32(opt + 28)
else:
    sys.exit("未知的 PE 可选头")
ndd = u32(dd - 4)
secs = []
for i in range(nsec):
    o = opt + optsz + 40 * i
    vsize, va, rawsize, rawptr = struct.unpack_from("<IIII", d, o + 8)
    secs.append((va, max(vsize, rawsize), rawptr))
def off(rva):
    for va, size, raw in secs:
        if va <= rva < va + size:
            return rva - va + raw
    raise ValueError("RVA 不在任何节内：%#x" % rva)
def cstr(rva):
    o = off(rva)
    return d[o:d.index(b"\0", o)].decode("ascii")
names = []
if ndd > 1:
    rva = u32(dd + 8)
    if rva:
        o = off(rva)
        while True:
            desc = struct.unpack_from("<5I", d, o)
            if not any(desc):
                break
            names.append(cstr(desc[3]))
            o += 20
if ndd > 13:
    rva = u32(dd + 8 * 13)
    if rva:
        o = off(rva)
        while True:
            desc = struct.unpack_from("<8I", d, o)
            if not any(desc):
                break
            attrs, name = desc[0], desc[1]
            names.append(cstr(name if attrs & 1 else name - base))
            o += 32
for n in dict.fromkeys(names):
    print(n)
PY
}

# ---------- 解压官方归档 ----------

log "解压官方归档 ${ARCHIVE_NAME}"
case "${ARCHIVE_NAME}" in
*.tar.gz | *.tgz | *.tar.xz | *.txz | *.tar)
	tar -xf "${ARCHIVE}" -C "${SRC}"
	;;
*.zip)
	if command -v unzip >/dev/null 2>&1; then
		unzip -q "${ARCHIVE}" -d "${SRC}"
	else
		find_python || die "解压 zip 需要 unzip 或 python"
		py_unzip "${ARCHIVE}" "${SRC}"
	fi
	;;
*) die "不认识的归档格式：${ARCHIVE_NAME}" ;;
esac

found=$(find "${SRC}" -type f -path "*/bin/mysqldump${EXE}")
[ -n "${found}" ] || die "官方归档里没有 bin/mysqldump${EXE}"
[ "$(printf '%s\n' "${found}" | wc -l | tr -d ' ')" = "1" ] || die "官方归档里有多个 bin/mysqldump${EXE}：${found}"
SRCROOT=$(cd "$(dirname "${found}")/.." && pwd -P)
[ -f "${SRCROOT}/bin/mysql${EXE}" ] || die "官方归档里没有 bin/mysql${EXE}"
[ -f "${SRCROOT}/LICENSE" ] || die "官方归档里没有 LICENSE"
log "官方目录：${SRCROOT#"${WORK}"/}"

# ---------- Linux：ldd 收集 + patchelf ----------

is_glibc_core() {
	case "$1" in
	linux-vdso.so* | linux-gate.so* | ld-linux*.so* | ld64.so* | libc.so.* | libm.so.* | libmvec.so.* | \
		libpthread.so.* | libdl.so.* | librt.so.* | libresolv.so.* | libutil.so.* | libanl.so.* | \
		libnsl.so.* | libnss_*.so.* | libBrokenLocale.so.* | libc_malloc_debug.so.* | libthread_db.so.*)
		return 0
		;;
	esac
	return 1
}

collect_linux() {
	command -v ldd >/dev/null 2>&1 || die "缺少 ldd"
	command -v patchelf >/dev/null 2>&1 || die "缺少 patchelf（Ubuntu：apt-get install patchelf）"
	local libs="${WORK}/libs.tsv" b out line name path real
	: >"${libs}"
	for b in mysqldump mysql; do
		out=$(env -u LD_LIBRARY_PATH -u LD_PRELOAD ldd "${SRCROOT}/bin/${b}" 2>&1) || die "ldd bin/${b} 失败：${out}"
		if printf '%s\n' "${out}" | grep -q 'not found'; then
			die "官方 bin/${b} 有找不到的依赖：$(printf '%s\n' "${out}" | grep 'not found' | tr '\n' ' ')"
		fi
		while IFS= read -r line; do
			case "${line}" in
			*'=>'*) ;;
			*) continue ;; # vdso 与解释器 ld-linux 那两行没有 =>
			esac
			name=$(printf '%s\n' "${line}" | awk '{print $1}')
			path=$(printf '%s\n' "${line}" | awk '{print $3}')
			case "${path}" in
			/*) ;;
			*) continue ;;
			esac
			real=$(realpath_of "${path}")
			case "${real}" in
			"${SRCROOT}"/*)
				has_line "${name}	${real}" "${libs}" || printf '%s\t%s\n' "${name}" "${real}" >>"${libs}"
				;;
			*)
				if ! is_glibc_core "${name}"; then
					has_line "${name} => ${path}" "${WORK}/syslibs.txt" || printf '%s => %s\n' "${name}" "${path}" >>"${WORK}/syslibs.txt"
				fi
				;;
			esac
		done <<EOF
${out}
EOF
		cp "${SRCROOT}/bin/${b}" "${STAGE}/bin/${b}"
	done

	# 同一个文件名对应两个不同的库就无法放进同一个 lib/
	local dup
	dup=$(cut -f1 "${libs}" | sort | uniq -d)
	[ -z "${dup}" ] || die "同名依赖库指向不同文件：${dup}"

	while IFS='	' read -r name real; do
		[ -n "${name}" ] || continue
		cp -L "${real}" "${STAGE}/lib/${name}"
		log "收集依赖库 lib/${name}（官方 ${real#"${SRCROOT}"/}）"
	done <"${libs}"
	chmod 0755 "${STAGE}"/bin/* "${STAGE}"/lib/* 2>/dev/null || true

	# 去掉调试信息（minimal 包本来就没有；arm64 只有完整包，里面的程序带调试信息）。必须在 patchelf 之前做。
	if command -v strip >/dev/null 2>&1; then
		local f
		for f in "${STAGE}"/bin/* "${STAGE}"/lib/*; do
			[ -f "${f}" ] || continue
			strip --strip-debug "${f}" || die "strip 失败：${f}"
		done
	else
		warn "没有 strip，保留调试信息"
	fi

	for b in mysqldump mysql; do
		patchelf --set-rpath '$ORIGIN/../lib' "${STAGE}/bin/${b}"
	done
	local l n needs_origin nlist
	for l in "${STAGE}"/lib/*; do
		[ -f "${l}" ] || continue
		needs_origin=0
		nlist=$(patchelf --print-needed "${l}") || die "patchelf 读不了：${l}"
		for n in ${nlist}; do
			[ -f "${STAGE}/lib/${n}" ] && needs_origin=1
		done
		if [ "${needs_origin}" = "1" ]; then
			patchelf --set-rpath '$ORIGIN' "${l}"
		fi
	done
	MODS_ZH="bin/ 下两个程序的 RUNPATH 改为 \$ORIGIN/../lib；lib/ 里依赖同目录其他库的库，RUNPATH 改为 \$ORIGIN；用 strip --strip-debug 去掉调试信息（官方文件带有时）。"
	MODS_EN="RUNPATH of the two programs in bin/ set to \$ORIGIN/../lib; RUNPATH of libraries in lib/ that depend on sibling libraries set to \$ORIGIN; debug info removed with strip --strip-debug (when present in the official files)."
}

# ---------- macOS：otool 收集 + install_name_tool + 重新签名 ----------

OTOOL=""
INT=""

macho_rpaths() {
	"${OTOOL}" -l "$1" | awk '$1 == "cmd" { r = ($2 == "LC_RPATH"); next } r && $1 == "path" { print $2; r = 0 }'
}

macho_id() {
	"${OTOOL}" -D "$1" | sed -n '2p'
}

# 依赖列表（去掉文件自身的 install name）
macho_deps() {
	local id
	id=$(macho_id "$1")
	"${OTOOL}" -L "$1" | sed '1d' | grep -v ':$' | awk '{print $1}' | while IFS= read -r d; do
		[ -n "${id}" ] && [ "${d}" = "${id}" ] && continue
		printf '%s\n' "${d}"
	done | awk '!seen[$0]++'
}

# 把 @loader_path / @executable_path 展开成实际目录
expand_macho_path() {
	local p=$1 loader_dir=$2
	case "${p}" in
	@loader_path/* | @loader_path) p="${loader_dir}${p#@loader_path}" ;;
	@executable_path/* | @executable_path) p="${SRCROOT}/bin${p#@executable_path}" ;;
	esac
	printf '%s\n' "${p}"
}

# 解析一个依赖到官方目录内的实际文件；系统库输出空；解析不了直接失败
resolve_macho_dep() {
	local file=$1 dep=$2 dir rest rp cand
	dir=$(dirname "${file}")
	case "${dep}" in
	/usr/lib/* | /System/*)
		return 0
		;;
	@rpath/*)
		rest=${dep#@rpath/}
		# 先用依赖方自己的 rpath，再用加载链上程序的 rpath（@loader_path 相对程序所在目录）
		for rp in $(macho_rpaths "${file}"); do
			cand="$(expand_macho_path "${rp}" "${dir}")/${rest}"
			if [ -f "${cand}" ]; then
				realpath_of "${cand}"
				return 0
			fi
		done
		for rp in $(cat "${WORK}/exe-rpaths.txt"); do
			cand="${rp}/${rest}"
			if [ -f "${cand}" ]; then
				realpath_of "${cand}"
				return 0
			fi
		done
		;;
	@loader_path/* | @executable_path/*)
		cand=$(expand_macho_path "${dep}" "${dir}")
		if [ -f "${cand}" ]; then
			realpath_of "${cand}"
			return 0
		fi
		;;
	/*)
		if [ -f "${dep}" ]; then
			cand=$(realpath_of "${dep}")
			case "${cand}" in
			"${SRCROOT}"/*)
				printf '%s\n' "${cand}"
				return 0
				;;
			esac
			die "$(basename "${file}") 依赖归档外的非系统库：${dep}"
		fi
		cand="${SRCROOT}/lib/$(basename "${dep}")"
		if [ -f "${cand}" ]; then
			realpath_of "${cand}"
			return 0
		fi
		;;
	esac
	die "$(basename "${file}") 的依赖 ${dep} 在官方归档里找不到"
}

# 运行 install_name_tool，滤掉“会让签名失效”的提示（之后统一重新签名）
run_int() {
	if ! "${INT}" "$@" 2>"${WORK}/int.err"; then
		cat "${WORK}/int.err" >&2
		die "install_name_tool 失败：$*"
	fi
	grep -v 'invalidate the code signature' "${WORK}/int.err" >&2 || true
}

collect_darwin() {
	OTOOL=${OTOOL_BIN:-$(xcrun -f otool 2>/dev/null || command -v otool || true)}
	INT=${INSTALL_NAME_TOOL_BIN:-$(xcrun -f install_name_tool 2>/dev/null || command -v install_name_tool || true)}
	[ -n "${OTOOL}" ] || die "缺少 otool（安装 Xcode 命令行工具）"
	[ -n "${INT}" ] || die "缺少 install_name_tool（安装 Xcode 命令行工具）"
	command -v codesign >/dev/null 2>&1 || die "缺少 codesign"

	local queue="${WORK}/queue.txt" deps="${WORK}/deps.tsv" i f d r b rp dlist
	: >"${deps}"
	: >"${WORK}/exe-rpaths.txt"
	printf '%s\n%s\n' "${SRCROOT}/bin/mysqldump" "${SRCROOT}/bin/mysql" >"${queue}"
	for b in mysqldump mysql; do
		for rp in $(macho_rpaths "${SRCROOT}/bin/${b}"); do
			rp=$(expand_macho_path "${rp}" "${SRCROOT}/bin")
			has_line "${rp%/}" "${WORK}/exe-rpaths.txt" || printf '%s\n' "${rp%/}" >>"${WORK}/exe-rpaths.txt"
		done
	done

	# 广度优先递归：队列里每个文件的依赖解析到官方目录内的实际文件，记录“文件 依赖写法 实际文件”
	i=1
	while :; do
		f=$(sed -n "${i}p" "${queue}")
		[ -n "${f}" ] || break
		dlist=$(macho_deps "${f}") || die "otool 读不了：${f}"
		for d in ${dlist}; do
			r=$(resolve_macho_dep "${f}" "${d}")
			if [ -z "${r}" ]; then
				continue # 系统库
			fi
			case "${r}" in
			"${SRCROOT}"/*) ;;
			*) die "依赖解析到了官方目录之外：${d} -> ${r}" ;;
			esac
			printf '%s\t%s\t%s\n' "${f}" "${d}" "${r}" >>"${deps}"
			has_line "${r}" "${queue}" || printf '%s\n' "${r}" >>"${queue}"
		done
		i=$((i + 1))
	done

	# 库在 lib/ 里用实际文件名，所有引用统一改写，包里不留符号链接
	local libs="${WORK}/libs.txt" dup
	sed '1,2d' "${queue}" >"${libs}"
	dup=$(while IFS= read -r f; do basename "${f}"; done <"${libs}" | sort | uniq -d)
	[ -z "${dup}" ] || die "不同的依赖库重名：${dup}"

	cp "${SRCROOT}/bin/mysqldump" "${SRCROOT}/bin/mysql" "${STAGE}/bin/"
	while IFS= read -r f; do
		[ -n "${f}" ] || continue
		cp "${f}" "${STAGE}/lib/$(basename "${f}")"
		log "收集依赖库 lib/$(basename "${f}")（官方 ${f#"${SRCROOT}"/}）"
	done <"${libs}"
	chmod 0755 "${STAGE}"/bin/* "${STAGE}"/lib/* 2>/dev/null || true

	# 改写引用：程序 -> @rpath/<库>，rpath 只留 @loader_path/../lib；库 -> @loader_path/<库>，id 改为 @rpath/<库>
	local staged kind name
	while IFS= read -r f; do
		[ -n "${f}" ] || continue
		name=$(basename "${f}")
		if [ "${f}" = "${SRCROOT}/bin/mysqldump" ] || [ "${f}" = "${SRCROOT}/bin/mysql" ]; then
			kind=bin
			staged="${STAGE}/bin/${name}"
		else
			kind=lib
			staged="${STAGE}/lib/${name}"
			run_int -id "@rpath/${name}" "${staged}"
		fi
		while IFS='	' read -r from d r; do
			[ "${from}" = "${f}" ] || continue
			if [ "${kind}" = "bin" ]; then
				run_int -change "${d}" "@rpath/$(basename "${r}")" "${staged}"
			else
				run_int -change "${d}" "@loader_path/$(basename "${r}")" "${staged}"
			fi
		done <"${deps}"
		for rp in $(macho_rpaths "${staged}"); do
			run_int -delete_rpath "${rp}" "${staged}"
		done
		if [ "${kind}" = "bin" ]; then
			run_int -add_rpath "@loader_path/../lib" "${staged}"
		fi
	done <"${queue}"

	# 改过的 Mach-O 一律重新 ad-hoc 签名
	for f in "${STAGE}"/bin/* "${STAGE}"/lib/*; do
		[ -f "${f}" ] || continue
		codesign --force --sign - --identifier "$(basename "${f}")" "${f}" 2>"${WORK}/codesign.err" || {
			cat "${WORK}/codesign.err" >&2
			die "重新签名失败：${f}"
		}
	done

	# 静态核对：除了系统库，只允许引用包内 lib/ 里存在的库
	for f in "${STAGE}"/bin/* "${STAGE}"/lib/*; do
		[ -f "${f}" ] || continue
		dlist=$(macho_deps "${f}") || die "otool 读不了：${f}"
		for d in ${dlist}; do
			case "${d}" in
			/usr/lib/* | /System/*) ;;
			@rpath/* | @loader_path/*)
				[ -f "${STAGE}/lib/${d#*/}" ] || die "${f#"${STAGE}"/} 引用了包里没有的库：${d}"
				;;
			*) die "${f#"${STAGE}"/} 仍有不可移植的引用：${d}" ;;
			esac
		done
		codesign --verify --strict "${f}" || die "签名校验不通过：${f#"${STAGE}"/}"
	done
	MODS_ZH="用 install_name_tool 把程序对依赖库的引用改为 @rpath/<库>、rpath 只保留 @loader_path/../lib；库之间的引用改为 @loader_path/<库>，库的 install name 改为 @rpath/<库>；改动后对这些文件重新做了 ad-hoc 代码签名（codesign -s -），原 Oracle 签名因此不再保留。"
	MODS_EN="install_name_tool was used to point the programs at @rpath/<lib> with a single rpath @loader_path/../lib, to point libraries at each other via @loader_path/<lib>, and to set library install names to @rpath/<lib>; the changed files were then re-signed ad hoc (codesign -s -), so the original Oracle signatures are no longer present."
}

# ---------- Windows：PE 导入表收集 ----------

collect_windows() {
	find_python || die "缺少 python（读 PE 导入表用）"
	local index="${WORK}/dll-index.tsv" queue="${WORK}/queue.txt" bundled="${WORK}/dlls.txt" i f n ln hit dir ilist
	: >"${index}"
	: >"${bundled}"
	# 官方 bin/ 优先（与 Windows 加载器先找程序目录一致），其次 lib/ 顶层
	for dir in "${SRCROOT}/bin" "${SRCROOT}/lib"; do
		[ -d "${dir}" ] || continue
		find "${dir}" -maxdepth 1 -type f -iname '*.dll' | LC_ALL=C sort | while IFS= read -r f; do
			printf '%s\t%s\n' "$(lower "$(basename "${f}")")" "${f}"
		done >>"${index}"
	done
	printf '%s\n%s\n' "${SRCROOT}/bin/mysqldump.exe" "${SRCROOT}/bin/mysql.exe" >"${queue}"
	i=1
	while :; do
		f=$(sed -n "${i}p" "${queue}")
		[ -n "${f}" ] || break
		ilist=$(pe_imports "${f}") || die "读不了 PE 导入表：${f}"
		for n in $(printf '%s\n' "${ilist}" | tr -d '\r'); do
			ln=$(lower "${n}")
			hit=$(awk -F '\t' -v k="${ln}" '$1 == k { print $2; exit }' "${index}")
			if [ -n "${hit}" ]; then
				if ! has_line "${hit}" "${queue}"; then
					printf '%s\n' "${hit}" >>"${queue}"
					printf '%s\n' "${hit}" >>"${bundled}"
				fi
			else
				has_line "${n}" "${WORK}/syslibs.txt" || printf '%s\n' "${n}" >>"${WORK}/syslibs.txt"
			fi
		done
		i=$((i + 1))
	done
	cp "${SRCROOT}/bin/mysqldump.exe" "${SRCROOT}/bin/mysql.exe" "${STAGE}/bin/"
	while IFS= read -r f; do
		[ -n "${f}" ] || continue
		[ -f "${STAGE}/bin/$(basename "${f}")" ] && die "DLL 重名：$(basename "${f}")"
		cp "${f}" "${STAGE}/bin/$(basename "${f}")"
		log "收集 DLL bin/$(basename "${f}")（官方 ${f#"${SRCROOT}"/}）"
	done <"${bundled}"
	if grep -iqE '^(vcruntime|msvcp)[0-9]' "${WORK}/syslibs.txt"; then
		warn "程序依赖 Microsoft Visual C++ 运行库（$(grep -iE '^(vcruntime|msvcp)[0-9]' "${WORK}/syslibs.txt" | tr '\n' ' ')），官方包里没有，需要系统已安装"
	fi
	MODS_ZH="没有改动任何文件；程序需要的 DLL 与程序一起放在 bin/。"
	MODS_EN="No file was modified; the DLLs the programs need are placed next to them in bin/."
}

case "${OS}" in
linux) collect_linux ;;
darwin) collect_darwin ;;
windows) collect_windows ;;
esac

if [ -s "${WORK}/syslibs.txt" ]; then
	log "以下依赖由系统提供，未打包："
	sed 's/^/[提取]   /' "${WORK}/syslibs.txt" >&2
	if [ "${OS}" = "linux" ]; then
		warn "有非 glibc 的系统库依赖未打包（目标机器需自带）：$(tr '\n' ';' <"${WORK}/syslibs.txt")"
	fi
fi

# ---------- LICENSE 与 README ----------

cp "${SRCROOT}/LICENSE" "${STAGE}/LICENSE"
chmod 0644 "${STAGE}/LICENSE"

FILE_LIST=$(cd "${STAGE}" && find ${PKG_DIRS} -type f | LC_ALL=C sort | sed 's/^/  /')
if [ -s "${WORK}/syslibs.txt" ]; then
	SYS_LIST=$(sed 's/ => .*//; s/^/  /' "${WORK}/syslibs.txt")
else
	SYS_LIST="  （无）/ (none)"
fi
case "${OS}" in
linux)
	SYS_NOTE_ZH="glibc 基础库（libc、libm、libpthread、libdl、librt、ld-linux 等）之外，还需要系统提供下列库（这些库若从构建机打包，会要求比官方基线更新的 glibc，所以不打包；缺 libncurses.so.6 / libtinfo.so.6 时，Debian/Ubuntu 装 libncurses6，RHEL 系装 ncurses-libs）："
	SYS_NOTE_EN="Besides the glibc core libraries (libc, libm, libpthread, libdl, librt, ld-linux, ...), the system must provide the libraries below (copies taken from the build machine would require a newer glibc than the official baseline, so they are not included; if libncurses.so.6 / libtinfo.so.6 is missing, install libncurses6 on Debian/Ubuntu or ncurses-libs on RHEL-like systems):"
	;;
darwin)
	SYS_NOTE_ZH="只依赖 /usr/lib 与 /System 下的 macOS 系统库。"
	SYS_NOTE_EN="Only macOS system libraries under /usr/lib and /System are used."
	;;
windows)
	SYS_NOTE_ZH="以下 DLL 由 Windows 提供；其中 VCRUNTIME*/MSVCP* 属于 Microsoft Visual C++ 2015-2022 运行库（x64），Windows 默认不带，需要另行安装："
	SYS_NOTE_EN="The following DLLs come from Windows; VCRUNTIME*/MSVCP* belong to the Microsoft Visual C++ 2015-2022 Redistributable (x64), which Windows does not ship by default and must be installed separately:"
	;;
esac
if [ "${OS}" = "darwin" ]; then
	SYS_LIST=""
fi
ARCHIVE_URL_TEXT=${MYSQL_ARCHIVE_URL:-"（未记录）/ (not recorded)"}
VERIFY_TEXT=${MYSQL_VERIFY_METHOD:-"（未记录）/ (not recorded)"}

cat >"${STAGE}/README-lemi.txt" <<EOF
Lemi MySQL 客户端工具包 / Lemi MySQL client tools package
=========================================================

[中文]

本包供 Lemi（https://github.com/LemonITCN/lemi）调用 mysqldump 与 mysql 使用。
其中的程序与库全部取自 MySQL 官方发行包，Lemi 没有改动其中的程序代码。

  MySQL 版本：     ${VERSION}（${SERIES} 系列）
  平台：           ${PLATFORM}
  官方发行包：     ${ARCHIVE_NAME}
  官方下载地址：   ${ARCHIVE_URL_TEXT}
  官方包校验方式： ${VERIFY_TEXT}

许可证：MySQL 社区版以 GNU 通用公共许可证第 2 版（GPL-2.0）发布。
同目录的 LICENSE 原样取自上述官方发行包，包含 GPL-2.0 全文及第三方组件的许可声明。

对应源代码：
  ${SOURCE_URL}
  该版本被更新版本取代后，官方会把它移到：
  ${SOURCE_ARCHIVE_URL}

本包只提取了以下文件（另加 LICENSE 与本说明）：
${FILE_LIST}

为了解压到任意目录都能直接运行，只做了以下处理：
  ${MODS_ZH}

运行时由操作系统提供、未打包的库：${SYS_NOTE_ZH}
${SYS_LIST}

[English]

This package lets Lemi (https://github.com/LemonITCN/lemi) run mysqldump and mysql.
All programs and libraries in it are taken from the official MySQL distribution;
Lemi did not change any program code.

  MySQL version:      ${VERSION} (${SERIES} series)
  Platform:           ${PLATFORM}
  Official package:   ${ARCHIVE_NAME}
  Official URL:       ${ARCHIVE_URL_TEXT}
  Verified with:      ${VERIFY_TEXT}

License: MySQL Community Edition is released under the GNU General Public License,
version 2 (GPL-2.0). The LICENSE file next to this one is copied unchanged from the
official package and contains the full GPL-2.0 text and the third-party notices.

Corresponding source code:
  ${SOURCE_URL}
  After a newer release supersedes this version, Oracle moves it to:
  ${SOURCE_ARCHIVE_URL}

Only these files were extracted (plus LICENSE and this README):
${FILE_LIST}

To make the package run from any directory, only the following was changed:
  ${MODS_EN}

Libraries expected from the operating system (not included): ${SYS_NOTE_EN}
${SYS_LIST}
EOF
chmod 0644 "${STAGE}/README-lemi.txt"

# ---------- 打包 ----------

# 固定时间戳与文件顺序，同样的输入得到同样的包
find "${STAGE}" -exec env TZ=UTC touch -t 202001010000 {} +
LIST="${WORK}/filelist.txt"
(cd "${STAGE}" && find ${PKG_DIRS} LICENSE README-lemi.txt | LC_ALL=C sort) >"${LIST}"
PKG="${WORK}/${PKG_NAME}"
log "打包 ${PKG_NAME}"
if [ "${PKG_EXT}" = "zip" ]; then
	find_python || die "打 zip 需要 python"
	(cd "${STAGE}" && py_zip "${PKG}" "${LIST}")
else
	if tar --version 2>/dev/null | grep -q 'GNU tar'; then
		(cd "${STAGE}" && tar --format=ustar --owner=0 --group=0 --numeric-owner --no-recursion -cf - -T "${LIST}") | gzip -n -9 >"${PKG}"
	else
		(cd "${STAGE}" && COPYFILE_DISABLE=1 tar --format=ustar --uid 0 --gid 0 --no-recursion -cf - -T "${LIST}") | gzip -n -9 >"${PKG}"
	fi
fi

# ---------- 自检：解压打包结果、换个路径运行 ----------

selftest() {
	local t="${WORK}/selftest" root out b expect_ver line name path loaded
	mkdir -p "${t}/unpacked"
	if [ "${PKG_EXT}" = "zip" ]; then
		py_unzip "${PKG}" "${t}/unpacked"
	else
		tar -xzf "${PKG}" -C "${t}/unpacked"
	fi
	# 移到另一个路径再运行，证明不依赖解压位置
	mv "${t}/unpacked" "${t}/moved-elsewhere"
	root=$(cd "${t}/moved-elsewhere" && pwd -P)

	local want got
	if [ "${OS}" = "windows" ]; then
		want=$(printf '%s\n' LICENSE README-lemi.txt bin)
	else
		want=$(printf '%s\n' LICENSE README-lemi.txt bin lib)
	fi
	got=$(cd "${root}" && ls -A | LC_ALL=C sort)
	[ "${got}" = "${want}" ] || die "包的顶层不对：$(printf '%s' "${got}" | tr '\n' ' ')"
	if find "${root}" -type l | grep -q .; then
		die "包里不应有符号链接"
	fi

	# 四段版本号（如 8.4.13.1）是官方补丁重发版，程序自报的仍是前三段（Ver 8.4.13），两种都认
	expect_ver=$(printf '%s' "${VERSION}" | sed 's/\./\\./g')
	case "${VERSION}" in
	*.*.*.*) expect_ver="${expect_ver}|$(printf '%s' "${VERSION}" | cut -d. -f1-3 | sed 's/\./\\./g')" ;;
	esac
	expect_ver="(${expect_ver})"
	for b in mysqldump mysql; do
		case "${OS}" in
		linux) out=$(cd / && env -u LD_LIBRARY_PATH -u LD_PRELOAD "${root}/bin/${b}" --version 2>&1) ;;
		darwin) out=$(cd / && env -u DYLD_LIBRARY_PATH -u DYLD_FALLBACK_LIBRARY_PATH -u DYLD_INSERT_LIBRARIES "${root}/bin/${b}" --version 2>&1) ;;
		windows)
			# 只留系统目录，防止从 PATH 里别的 MySQL 安装找到 DLL 掩盖缺库
			local sysroot
			sysroot=$(cygpath -u "${SYSTEMROOT:-C:\\Windows}")
			out=$(cd / && env PATH="${sysroot}/System32:${sysroot}" "${root}/bin/${b}.exe" --version 2>&1)
			;;
		esac || die "自检：bin/${b}${EXE} --version 运行失败：${out}"
		log "自检：${out}"
		printf '%s\n' "${out}" | grep -Eq "(^|[^0-9.])${expect_ver}([^0-9.]|$)" || die "自检：bin/${b}${EXE} --version 输出里没有版本号 ${VERSION}：${out}"
	done

	if [ "${OS}" = "linux" ]; then
		for b in mysqldump mysql; do
			out=$(env -u LD_LIBRARY_PATH -u LD_PRELOAD ldd "${root}/bin/${b}" 2>&1) || die "自检：ldd bin/${b} 失败：${out}"
			if printf '%s\n' "${out}" | grep -q 'not found'; then
				die "自检：bin/${b} 有找不到的库：$(printf '%s\n' "${out}" | grep 'not found' | tr '\n' ' ')"
			fi
			# 包里带的库必须从包里加载，而不是从系统里找到同名库
			while IFS= read -r line; do
				case "${line}" in *'=>'*) ;; *) continue ;; esac
				name=$(printf '%s\n' "${line}" | awk '{print $1}')
				path=$(printf '%s\n' "${line}" | awk '{print $3}')
				if [ -f "${root}/lib/${name}" ] && [ "$(realpath_of "${path}")" != "${root}/lib/${name}" ]; then
					die "自检：bin/${b} 的 ${name} 没有从包内 lib/ 加载，而是 ${path}"
				fi
			done <<EOF
${out}
EOF
		done
		log "自检：ldd 无 not found，包内库都从包内加载"
	fi

	if [ "${OS}" = "darwin" ]; then
		for b in mysqldump mysql; do
			out=$(cd / && env -u DYLD_LIBRARY_PATH -u DYLD_FALLBACK_LIBRARY_PATH DYLD_PRINT_LIBRARIES=1 "${root}/bin/${b}" --version 2>&1 >/dev/null || true)
			if ! printf '%s\n' "${out}" | grep -q '^dyld'; then
				warn "自检：DYLD_PRINT_LIBRARIES 没有输出，跳过 bin/${b} 的加载路径核对"
				continue
			fi
			for f in "${root}"/lib/*; do
				[ -f "${f}" ] || continue
				name=$(basename "${f}")
				# 每行形如 “dyld[进程号]: <UUID> /路径”，较早的系统没有 <UUID>
				loaded=$(printf '%s\n' "${out}" | sed -n 's/^dyld[^:]*: //p' | sed 's/^<[^>]*> //' | grep "/${name}\$" || true)
				[ -n "${loaded}" ] || continue # 这个程序不用这个库
				while IFS= read -r path; do
					[ "${path#/private}" = "${root#/private}/lib/${name}" ] || die "自检：bin/${b} 的 ${name} 没有从包内加载，而是 ${path}"
				done <<EOF
${loaded}
EOF
			done
		done
		log "自检：dyld 实际加载的包内库都来自包内 lib/"
	fi
}
selftest

# ---------- 输出 ----------

SHA=$(sha256_of "${PKG}")
cp "${PKG}" "${OUT}/${PKG_NAME}"
printf '{"version":"%s","series":"%s","tag":"%s","platform":"%s","name":"%s","sha256":"%s"}\n' \
	"${VERSION}" "${SERIES}" "${TAG}" "${PLATFORM}" "${PKG_NAME}" "${SHA}" >"${OUT}/${MANIFEST_NAME}"

log "包内文件："
(cd "${STAGE}" && find ${PKG_DIRS} LICENSE README-lemi.txt -type f | LC_ALL=C sort | sed 's/^/[提取]   /') >&2
log "完成：${OUT}/${PKG_NAME}"
log "SHA-256：${SHA}"
log "清单片段：${OUT}/${MANIFEST_NAME}"
printf '%s  %s\n' "${SHA}" "${PKG_NAME}"
