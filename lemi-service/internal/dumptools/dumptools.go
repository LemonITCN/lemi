// Package dumptools 管理 mysqldump / mysql 客户端（docs/spec.md §13）：按清单从 Release 下载对应平台的工具包、
// 校验 SHA-256、安全解压到 <数据目录>/tools/mysql-<版本>/<os-arch>/，按服务器版本选择客户端版本，
// 生成临时的 --defaults-extra-file（口令不上命令行），经 SSH 隧道 / 代理的连接在本机回环地址开临时转发。
package dumptools

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

//go:embed manifest.json
var manifestJSON []byte

// File 是一个平台的工具包。
type File struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
}

// Version 是一个客户端版本。
type Version struct {
	Version string          `json:"version"` // 例如 8.4.13.1（官方补丁重发版有四段）
	Series  string          `json:"series"`  // 例如 8.4
	Tag     string          `json:"tag"`     // Release 标签，例如 tools-mysql-8.4.13.1
	Files   map[string]File `json:"files"`   // 平台 → 文件
}

// Manifest 是随代码发布的工具清单。
type Manifest struct {
	Versions []Version `json:"versions"`
}

// ParseManifest 解析清单并校验字段。
func ParseManifest(b []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("mysqldump 工具清单格式不正确：%w", err)
	}
	for i, v := range m.Versions {
		if !validVersion(v.Version) || v.Tag == "" || strings.ContainsAny(v.Tag, "/\\") {
			return nil, fmt.Errorf("mysqldump 工具清单第 %d 项不正确", i+1)
		}
		if v.Series == "" {
			m.Versions[i].Series = seriesOf(v.Version)
		}
		for p, f := range v.Files {
			if f.Name == "" || strings.ContainsAny(f.Name, "/\\") || len(f.SHA256) != 64 {
				return nil, fmt.Errorf("mysqldump 工具清单 %s/%s 不正确", v.Version, p)
			}
		}
	}
	return &m, nil
}

// Builtin 返回随程序发布的清单。
func Builtin() *Manifest {
	m, err := ParseManifest(manifestJSON)
	if err != nil {
		panic(err)
	}
	return m
}

func validVersion(v string) bool {
	if v == "" || len(v) > 32 {
		return false
	}
	parts := strings.Split(v, ".")
	if len(parts) < 2 || len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, r := range p {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func seriesOf(v string) string {
	p := strings.Split(v, ".")
	if len(p) < 2 {
		return v
	}
	return p[0] + "." + p[1]
}

// versionKey 把版本号转成可比较的整数序列。
func versionKey(v string) []int {
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, _ := strconv.Atoi(p)
		out = append(out, n)
	}
	for len(out) < 4 {
		out = append(out, 0)
	}
	return out
}

// CompareVersions 比较两个版本号（-1 / 0 / 1）。
func CompareVersions(a, b string) int {
	ka, kb := versionKey(a), versionKey(b)
	for i := 0; i < len(ka) && i < len(kb); i++ {
		if ka[i] != kb[i] {
			if ka[i] < kb[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

// ErrNoPlatform 当前平台没有可用的 mysqldump。
var ErrNoPlatform = errors.New("本平台没有可用的 mysqldump，请改用 Lemi 自研导出器")

// Platform 返回当前平台在清单里的键。windows-arm64 在 Windows 11 上用 x64 仿真运行 windows-amd64 版；
// Windows 10 ARM 没有 x64 仿真，返回 ErrNoPlatform。
func Platform() (string, error) {
	return platformFor(runtime.GOOS, runtime.GOARCH, windowsBuild())
}

func platformFor(goos, goarch string, winBuild uint32) (string, error) {
	switch goos {
	case "linux", "darwin":
		if goarch == "amd64" || goarch == "arm64" {
			return goos + "-" + goarch, nil
		}
	case "windows":
		switch goarch {
		case "amd64":
			return "windows-amd64", nil
		case "arm64":
			// Windows 11（内部版本 22000 起）支持 x64 仿真
			if winBuild >= 22000 {
				return "windows-amd64", nil
			}
		}
	}
	return "", ErrNoPlatform
}

// Pick 按服务器产品与版本选择客户端版本（docs/spec.md §13）：
// MySQL 5.x 与 8.0 用 8.0 客户端，8.1–8.4 用 8.4，9.x 用清单里最新的 9.x，更新的版本（如 26.x）用同主版本里最新的；
// MariaDB 用 8.0 客户端（并给出警告，建议用自研导出器）。清单里没有精确系列（或该系列缺当前平台）时
// 退而用不低于目标系列的最老可用版本；都没有返回错误。
func (m *Manifest) Pick(product, serverVersion, platform string) (*Version, []string, error) {
	var warns []string
	major, minor := parseServerVersion(serverVersion)
	mariadb := strings.EqualFold(product, "MariaDB") || strings.Contains(strings.ToLower(serverVersion), "mariadb")
	want := ""
	switch {
	case mariadb:
		want = "8.0"
		warns = append(warns, "MariaDB 服务器建议使用 Lemi 自研导出器：MySQL 的 mysqldump 与 MariaDB 并不完全兼容（序列、系统版本表等可能导不全）")
	case major == 0:
		return nil, nil, fmt.Errorf("无法识别服务器版本 %q", serverVersion)
	case major < 8 || (major == 8 && minor == 0):
		want = "8.0"
	case major == 8:
		want = "8.4"
	}
	avail := func(v *Version) bool { _, ok := v.Files[platform]; return ok }
	var cands []*Version
	for i := range m.Versions {
		if avail(&m.Versions[i]) {
			cands = append(cands, &m.Versions[i])
		}
	}
	if len(cands) == 0 {
		return nil, warns, fmt.Errorf("工具清单里没有适用于本平台（%s）的 mysqldump，请改用 Lemi 自研导出器，或在系统设置里指定服务器上已安装的 mysqldump", platform)
	}
	sort.Slice(cands, func(i, j int) bool { return CompareVersions(cands[i].Version, cands[j].Version) < 0 })
	if want != "" {
		var best *Version
		for _, v := range cands {
			if v.Series == want {
				best = v // 同系列取最新
			}
		}
		if best != nil {
			return best, warns, nil
		}
		for _, v := range cands { // 不低于目标系列的最老版本
			if CompareVersions(v.Series, want) > 0 {
				warns = append(warns, fmt.Sprintf("工具清单里没有 %s 系列的客户端，改用 %s", want, v.Version))
				return v, warns, nil
			}
		}
		return nil, warns, fmt.Errorf("工具清单里没有适用于 MySQL %s 的 mysqldump", serverVersion)
	}
	// 9.x 及以后：同主版本里最新的
	var best *Version
	for _, v := range cands {
		if vm, _ := parseServerVersion(v.Version); vm == major {
			best = v
		}
	}
	if best != nil {
		return best, warns, nil
	}
	for _, v := range cands {
		if vm, _ := parseServerVersion(v.Version); vm > major {
			warns = append(warns, fmt.Sprintf("工具清单里没有 %d.x 的客户端，改用 %s", major, v.Version))
			return v, warns, nil
		}
	}
	latest := cands[len(cands)-1]
	warns = append(warns, fmt.Sprintf("服务器版本 %s 比工具清单里最新的客户端 %s 还新，可能有不兼容的语法", serverVersion, latest.Version))
	return latest, warns, nil
}

// parseServerVersion 取主、次版本号（兼容 MariaDB 的 5.5.5- 前缀）。
func parseServerVersion(v string) (int, int) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "5.5.5-")
	parts := strings.SplitN(v, ".", 3)
	num := func(s string) int {
		n := 0
		for _, r := range s {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	if len(parts) < 2 {
		return num(v), 0
	}
	return num(parts[0]), num(parts[1])
}

// Is57 服务器是否 MySQL 5.x（mysqldump 8.x 需要 --column-statistics=0）。
func Is57(product, serverVersion string) bool {
	major, _ := parseServerVersion(serverVersion)
	return !strings.EqualFold(product, "MariaDB") && major > 0 && major < 8
}
