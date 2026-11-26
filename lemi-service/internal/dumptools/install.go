package dumptools

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 解压上限（防解压炸弹）。官方客户端加依赖库解压后约 30–60 MB。
var (
	MaxDownload    int64 = 512 << 20
	MaxUnpacked    int64 = 1 << 30
	MaxFiles             = 500
	MaxFileSize    int64 = 512 << 20
	downloadTimout       = 30 * time.Minute
)

// Tool 是一套可用的客户端。
type Tool struct {
	Version   string `json:"version"`
	Series    string `json:"series"`
	Mysqldump string `json:"mysqldump"`
	Mysql     string `json:"mysql"`
	// MariaDB 客户端是 MariaDB 的（只会出现在管理员指定的路径里），没有 --set-gtid-purged、--column-statistics、--ssl-mode。
	MariaDB bool   `json:"mariadb,omitempty"`
	Source  string `json:"source"` // bundled | custom
}

// Custom 是管理员在设置里指定的服务器上已安装的客户端。
type Custom struct {
	Mysqldump string `json:"mysqldump_path"`
	Mysql     string `json:"mysql_path"`
}

// Manager 管理工具包的下载与查找。
type Manager struct {
	Dir      string // <数据目录>/tools
	BaseURL  string // 下载地址前缀，文件地址为 <BaseURL>/<tag>/<name>
	Client   *http.Client
	Manifest *Manifest
	// Preinstalled 只读的预装目录（Docker 镜像里的 /opt/lemi/tools），布局与 Dir 相同：<目录>/mysql-<版本>/<平台>/bin/…
	Preinstalled string
	// Custom 返回管理员指定的客户端路径（可为 nil）。
	Custom func() Custom
	mu     sync.Mutex
	locks  map[string]*sync.Mutex
}

// NewManager 创建管理器。
func NewManager(dir, baseURL string) *Manager {
	return &Manager{Dir: dir, BaseURL: strings.TrimRight(baseURL, "/"), Manifest: Builtin(), Client: &http.Client{Timeout: downloadTimout}, locks: map[string]*sync.Mutex{}}
}

func (m *Manager) lock(key string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.locks == nil {
		m.locks = map[string]*sync.Mutex{}
	}
	l := m.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		m.locks[key] = l
	}
	return l
}

func exeName(name, platform string) string {
	if strings.HasPrefix(platform, "windows") {
		return name + ".exe"
	}
	return name
}

// InstallDir 是某版本某平台的解压目录。
func (m *Manager) InstallDir(version, platform string) string {
	return filepath.Join(m.Dir, "mysql-"+version, platform)
}

// Installed 返回已安装的工具（没有返回 nil）：先看数据目录里下载的（带完成标记），再看预装目录。
func (m *Manager) Installed(v *Version, platform string) *Tool {
	if t := toolAt(m.InstallDir(v.Version, platform), v, platform, true); t != nil {
		return t
	}
	if m.Preinstalled != "" {
		if t := toolAt(filepath.Join(m.Preinstalled, "mysql-"+v.Version, platform), v, platform, false); t != nil {
			t.Source = "preinstalled"
			return t
		}
	}
	return nil
}

func toolAt(dir string, v *Version, platform string, needMarker bool) *Tool {
	t := &Tool{Version: v.Version, Series: v.Series, Source: "bundled",
		Mysqldump: filepath.Join(dir, "bin", exeName("mysqldump", platform)), Mysql: filepath.Join(dir, "bin", exeName("mysql", platform))}
	for _, p := range []string{t.Mysqldump, t.Mysql} {
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return nil
		}
	}
	if needMarker {
		if _, err := os.Stat(filepath.Join(dir, ".lemi-ok")); err != nil {
			return nil
		}
	}
	return t
}

// Find 按版本号找清单项。
func (m *Manager) Find(version string) *Version {
	for i := range m.Manifest.Versions {
		if m.Manifest.Versions[i].Version == version {
			return &m.Manifest.Versions[i]
		}
	}
	return nil
}

// Install 下载、校验并解压一个版本（已安装则直接返回）。progress 可为空，参数是已下载字节与总字节（未知为 -1）。
func (m *Manager) Install(ctx context.Context, v *Version, platform string, progress func(done, total int64)) (*Tool, error) {
	f, ok := v.Files[platform]
	if !ok {
		return nil, fmt.Errorf("mysqldump %s 没有本平台（%s）的工具包", v.Version, platform)
	}
	l := m.lock(v.Version + "/" + platform)
	l.Lock()
	defer l.Unlock()
	if t := m.Installed(v, platform); t != nil {
		return t, nil
	}
	if err := os.MkdirAll(m.Dir, 0o700); err != nil {
		return nil, err
	}
	tmpDir, err := os.MkdirTemp(m.Dir, ".download-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	archive := filepath.Join(tmpDir, "pkg")
	if err := m.download(ctx, v.Tag, f, archive, progress); err != nil {
		return nil, err
	}
	stage := filepath.Join(tmpDir, "stage")
	if err := os.MkdirAll(stage, 0o700); err != nil {
		return nil, err
	}
	if strings.HasSuffix(f.Name, ".zip") {
		err = extractZip(archive, stage)
	} else {
		err = extractTarGz(archive, stage)
	}
	if err != nil {
		return nil, fmt.Errorf("解压 mysqldump 工具包失败：%w", err)
	}
	for _, name := range []string{"mysqldump", "mysql"} {
		p := filepath.Join(stage, "bin", exeName(name, platform))
		if fi, err := os.Stat(p); err != nil || !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("mysqldump 工具包里缺少 bin/%s", exeName(name, platform))
		}
		_ = os.Chmod(p, 0o755)
	}
	if err := os.WriteFile(filepath.Join(stage, ".lemi-ok"), []byte(f.SHA256+"\n"), 0o600); err != nil {
		return nil, err
	}
	final := m.InstallDir(v.Version, platform)
	_ = os.RemoveAll(final) // 上次装了一半的残留
	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, final); err != nil {
		return nil, err
	}
	t := m.Installed(v, platform)
	if t == nil {
		return nil, errors.New("安装 mysqldump 后找不到程序")
	}
	return t, nil
}

func (m *Manager) download(ctx context.Context, tag string, f File, dst string, progress func(done, total int64)) error {
	url := m.BaseURL + "/" + tag + "/" + f.Name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "lemi-dumptools")
	cl := m.Client
	if cl == nil {
		cl = http.DefaultClient
	}
	resp, err := cl.Do(req)
	if err != nil {
		return fmt.Errorf("下载 mysqldump 工具包失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("下载 mysqldump 工具包失败：%s 返回 HTTP %d", url, resp.StatusCode)
	}
	if resp.ContentLength > MaxDownload {
		return errors.New("mysqldump 工具包超过大小上限")
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	h := sha256.New()
	var done int64
	buf := make([]byte, 256<<10)
	body := io.LimitReader(resp.Body, MaxDownload+1)
	for {
		n, rerr := body.Read(buf)
		if n > 0 {
			done += int64(n)
			if done > MaxDownload {
				out.Close()
				return errors.New("mysqldump 工具包超过大小上限")
			}
			h.Write(buf[:n])
			if _, err := out.Write(buf[:n]); err != nil {
				out.Close()
				return err
			}
			if progress != nil {
				progress(done, resp.ContentLength)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			out.Close()
			return fmt.Errorf("下载 mysqldump 工具包中断：%w", rerr)
		}
	}
	if err := out.Close(); err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, f.SHA256) {
		return fmt.Errorf("mysqldump 工具包校验失败：SHA-256 应为 %s，实际 %s（文件可能被篡改或下载不完整，已删除）", f.SHA256, got)
	}
	return nil
}

// safeRel 校验归档里的路径：相对、不含 ..、不含反斜杠与盘符，返回清理后的相对路径（"" 表示跳过）。
func safeRel(name string) (string, error) {
	if strings.Contains(name, "\\") || strings.Contains(name, ":") || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("归档里的路径 %q 不合法", name)
	}
	if strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("归档里的路径 %q 是绝对路径", name)
	}
	c := path.Clean(name)
	if c == "." {
		return "", nil
	}
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", fmt.Errorf("归档里的路径 %q 越出了解压目录", name)
	}
	return c, nil
}

type extractBudget struct {
	files int
	bytes int64
}

func (b *extractBudget) file(size int64) error {
	b.files++
	if b.files > MaxFiles {
		return errors.New("归档里的文件太多")
	}
	if size > MaxFileSize {
		return errors.New("归档里有超大文件")
	}
	return nil
}

// writeFile 把 r 写到 dst，最多 MaxFileSize 字节并计入总量上限（不信任头部声明的大小）。
func (b *extractBudget) writeFile(dst string, r io.Reader, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	n, err := io.Copy(f, io.LimitReader(r, MaxFileSize+1))
	cerr := f.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if n > MaxFileSize {
		return errors.New("归档里有超大文件")
	}
	b.bytes += n
	if b.bytes > MaxUnpacked {
		return errors.New("归档解压后超过大小上限")
	}
	return nil
}

func extractTarGz(src, dst string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var b extractBudget
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		rel, err := safeRel(h.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := b.file(h.Size); err != nil {
				return err
			}
			mode := os.FileMode(0o600)
			if h.FileInfo().Mode()&0o111 != 0 {
				mode = 0o700
			}
			if err := b.writeFile(target, tr, mode); err != nil {
				return err
			}
		default:
			// 符号链接、硬链接、设备文件一律拒绝（工具包里不应该有）
			return fmt.Errorf("归档里有不允许的条目 %q（类型 %c）", h.Name, h.Typeflag)
		}
	}
}

func extractZip(src, dst string) error {
	zr, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer zr.Close()
	var b extractBudget
	for _, zf := range zr.File {
		rel, err := safeRel(zf.Name)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		mode := zf.Mode()
		switch {
		case mode.IsDir():
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
			continue
		case !mode.IsRegular():
			return fmt.Errorf("归档里有不允许的条目 %q", zf.Name)
		}
		if err := b.file(int64(zf.UncompressedSize64)); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		err = b.writeFile(target, rc, 0o700)
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// Remove 删除已安装的版本（所有平台）。
func (m *Manager) Remove(version string) error {
	if !validVersion(version) {
		return errors.New("版本号不正确")
	}
	return os.RemoveAll(filepath.Join(m.Dir, "mysql-"+version))
}

// VersionStatus 是管理页上的一行。
type VersionStatus struct {
	Version   string `json:"version"`
	Series    string `json:"series"`
	Available bool   `json:"available"` // 清单里有本平台的包
	Installed bool   `json:"installed"`
	Source    string `json:"source,omitempty"` // bundled（数据目录里下载的）| preinstalled（镜像预装）
	File      string `json:"file,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"` // 已安装的占用
}

// Status 列出清单里的全部版本与安装情况。
func (m *Manager) Status() (platform string, platformErr error, list []VersionStatus) {
	platform, platformErr = Platform()
	for i := range m.Manifest.Versions {
		v := &m.Manifest.Versions[i]
		st := VersionStatus{Version: v.Version, Series: v.Series}
		if f, ok := v.Files[platform]; ok && platformErr == nil {
			st.Available, st.File = true, f.Name
			if t := m.Installed(v, platform); t != nil {
				st.Installed, st.Source = true, t.Source
				st.SizeBytes = dirSize(filepath.Dir(filepath.Dir(t.Mysqldump)))
			}
		}
		list = append(list, st)
	}
	return
}

func dirSize(dir string) int64 {
	var n int64
	_ = filepath.Walk(dir, func(_ string, fi os.FileInfo, err error) error {
		if err == nil && fi.Mode().IsRegular() {
			n += fi.Size()
		}
		return nil
	})
	return n
}

// ---------------- 服务器上已安装的客户端 ----------------

var (
	reMySQLVer   = regexp.MustCompile(`Ver\s+(\d+\.\d+\.\d+)`)
	reDistrib    = regexp.MustCompile(`Distrib\s+(\d+\.\d+\.\d+)(-MariaDB)?`)
	reMariaFrom  = regexp.MustCompile(`from\s+(\d+\.\d+\.\d+)-MariaDB`)
	errNotClient = errors.New("不是可用的 mysqldump/mysql 程序")
)

// DetectVersion 运行 `<程序> --version` 读取版本号；mariadb 表示是 MariaDB 的客户端。
func DetectVersion(ctx context.Context, exe string) (version string, mariadb bool, err error) {
	if !filepath.IsAbs(exe) {
		return "", false, errors.New("请填写程序的绝对路径")
	}
	fi, err := os.Stat(exe)
	if err != nil {
		return "", false, fmt.Errorf("程序不存在：%w", err)
	}
	if !fi.Mode().IsRegular() {
		return "", false, errors.New("不是普通文件")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, exe, "--version").CombinedOutput()
	if err != nil {
		return "", false, fmt.Errorf("运行 %s --version 失败：%v", filepath.Base(exe), err)
	}
	return parseVersionOutput(string(out))
}

func parseVersionOutput(s string) (string, bool, error) {
	mariadb := strings.Contains(s, "MariaDB")
	if m := reMariaFrom.FindStringSubmatch(s); m != nil {
		return m[1], true, nil
	}
	if m := reDistrib.FindStringSubmatch(s); m != nil {
		return m[1], mariadb || m[2] != "", nil
	}
	if m := reMySQLVer.FindStringSubmatch(s); m != nil {
		return m[1], mariadb, nil
	}
	return "", false, errNotClient
}

// CustomTool 校验并返回管理员指定的客户端（两项都没填返回 nil）。
func CustomTool(ctx context.Context, c Custom) (*Tool, error) {
	if c.Mysqldump == "" && c.Mysql == "" {
		return nil, nil
	}
	if c.Mysqldump == "" || c.Mysql == "" {
		return nil, errors.New("请同时指定 mysqldump 与 mysql 的路径")
	}
	v, maria, err := DetectVersion(ctx, c.Mysqldump)
	if err != nil {
		return nil, fmt.Errorf("mysqldump：%w", err)
	}
	if _, _, err := DetectVersion(ctx, c.Mysql); err != nil {
		return nil, fmt.Errorf("mysql：%w", err)
	}
	return &Tool{Version: v, Series: seriesOf(v), Mysqldump: c.Mysqldump, Mysql: c.Mysql, MariaDB: maria, Source: "custom"}, nil
}

// Resolve 为服务器选一套客户端：管理员指定了路径就用它；否则按清单选版本，没装且 autoInstall 时下载安装。
func (m *Manager) Resolve(ctx context.Context, product, serverVersion string, autoInstall bool, progress func(done, total int64)) (*Tool, []string, error) {
	if m.Custom != nil {
		if c := m.Custom(); c.Mysqldump != "" || c.Mysql != "" {
			t, err := CustomTool(ctx, c)
			if err != nil {
				return nil, nil, fmt.Errorf("系统设置里指定的客户端不可用：%w", err)
			}
			return t, nil, nil
		}
	}
	platform, err := Platform()
	if err != nil {
		return nil, nil, err
	}
	v, warns, err := m.Manifest.Pick(product, serverVersion, platform)
	if err != nil {
		return nil, warns, err
	}
	if t := m.Installed(v, platform); t != nil {
		return t, warns, nil
	}
	if !autoInstall {
		return nil, warns, fmt.Errorf("mysqldump %s 尚未下载", v.Version)
	}
	t, err := m.Install(ctx, v, platform, progress)
	return t, warns, err
}

// Available 当前平台是否可能用 mysqldump（管理员指定了路径，或清单里有本平台的包）。
func (m *Manager) Available() (bool, string) {
	if m.Custom != nil {
		if c := m.Custom(); c.Mysqldump != "" {
			return true, ""
		}
	}
	platform, err := Platform()
	if err != nil {
		return false, err.Error()
	}
	for _, v := range m.Manifest.Versions {
		if _, ok := v.Files[platform]; ok {
			return true, ""
		}
	}
	return false, fmt.Sprintf("工具清单里还没有本平台（%s）的 mysqldump，请改用 Lemi 自研导出器，或由实例管理员指定服务器上已安装的 mysqldump", platform)
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
