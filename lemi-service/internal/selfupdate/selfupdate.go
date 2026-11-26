// Package selfupdate 实现在线升级（docs/spec.md §20）：
//
//  1. 检查：请求 GitHub 版本列表，取 v* 标签里版本号最大的正式版，与编进程序的版本比较。
//  2. 下载：对应平台的发布包 lemi-<版本>-<系统>-<架构>.tar.gz|zip、SHA256SUMS、SHA256SUMS.sig。
//  3. 校验：用编进程序的 Ed25519 公钥验证 SHA256SUMS 的签名，再核对发布包的 SHA-256；
//     清单里必须有「最新版本号」对应的包名，旧版本的清单拿来冒充新版本会因找不到包名被拒绝；不接受降级与同版本重装。
//  4. 安装：解压出新的可执行文件，跑一次 `version` 确认能运行且版本对得上，再原子替换
//     （Windows 下运行中的 exe 先改名为 .old 再放新文件）。
//  5. 重启：见 restart.go。Docker 部署只提示，不自升级（由调用方判断 cfg.InDocker）。
package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LemonITCN/lemi/lemi-service/internal/version"
)

// DefaultAPIURL 是 GitHub 的版本列表接口。不用 releases/latest：同一个仓库里还有 tools-mysql-* 这类工具版本，
// GitHub 的「最新版」可能落到它们身上；这里自己挑 v* 标签里版本号最大的正式版（草稿与预发布版不算）。
const DefaultAPIURL = "https://api.github.com/repos/LemonITCN/lemi/releases?per_page=100"

// 发布清单文件名。
const (
	SumsName = "SHA256SUMS"
	SigName  = "SHA256SUMS.sig"
)

const (
	maxAPIBody     = 4 << 20
	maxSumsSize    = 1 << 20
	maxSigSize     = 4 << 10
	maxPackageSize = 512 << 20
	maxBinarySize  = 512 << 20
)

// 错误。
var (
	ErrNotNewer   = errors.New("没有比当前更新的版本（不接受降级或重复安装）")
	ErrChecksum   = errors.New("发布包的 SHA-256 与签名清单不一致，拒绝升级")
	ErrNoPackage  = errors.New("这个版本没有当前平台的发布包")
	ErrBusy       = errors.New("正在升级中")
	ErrNoRelease  = errors.New("还没有发布任何正式版")
	errBadArchive = errors.New("发布包里没有 Lemi 可执行文件")
)

// Asset 是 Release 里的一个文件。
type Asset struct {
	Name string
	URL  string
	Size int64
}

// Release 是一个已发布的正式版。
type Release struct {
	Version     string    `json:"version"`
	Tag         string    `json:"tag"`
	Name        string    `json:"name"`
	Notes       string    `json:"notes"`
	URL         string    `json:"url"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []Asset   `json:"-"`
}

func (r *Release) asset(name string) *Asset {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i]
		}
	}
	return nil
}

// Result 是一次升级的结果。
type Result struct {
	From string
	To   string
	Exe  string // 被替换的可执行文件（重启时运行它）
}

// Updater 检查并安装新版本。零值不可用，用 New 创建；测试可改各字段。
type Updater struct {
	APIURL    string
	Client    *http.Client
	PublicKey ed25519.PublicKey
	Current   string // 当前版本
	GOOS      string
	GOARCH    string
	// ExePath 要替换的可执行文件，空时取当前进程的可执行文件（解析符号链接）
	ExePath string
	// SkipExecCheck 跳过「运行新程序的 version 确认版本号」这一步（测试用假程序时）
	SkipExecCheck bool

	mu        sync.Mutex
	latest    *Release
	checkedAt time.Time
	checkErr  error
	applying  atomic.Bool
}

// New 创建使用 GitHub 与内置公钥的升级器。
func New() *Updater {
	return &Updater{
		APIURL:    DefaultAPIURL,
		Client:    DefaultClient(),
		PublicKey: ReleasePublicKey(),
		Current:   version.Version,
		GOOS:      runtime.GOOS,
		GOARCH:    runtime.GOARCH,
	}
}

// DefaultClient 是升级用的 HTTP 客户端：遵循 HTTPS_PROXY 等代理环境变量，重定向只允许 HTTPS。
// 下载整体时长由调用方的 context 控制。
func DefaultClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = http.ProxyFromEnvironment
	tr.TLSHandshakeTimeout = 20 * time.Second
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: tr, CheckRedirect: httpsOnlyRedirect}
}

func httpsOnlyRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("重定向次数过多")
	}
	if req.URL.Scheme != "https" {
		return errors.New("下载地址重定向到了非 HTTPS 地址，已拒绝")
	}
	return nil
}

// Supported 当前平台是否有官方发布包。
func Supported(goos, goarch string) bool {
	switch goos + "/" + goarch {
	case "linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64", "windows/amd64", "windows/arm64":
		return true
	}
	return false
}

// PackageName 返回发布包文件名：lemi-<版本>-<系统>-<架构>.tar.gz（Windows 为 .zip）。
func PackageName(ver, goos, goarch string) string {
	ext := ".tar.gz"
	if goos == "windows" {
		ext = ".zip"
	}
	return "lemi-" + strings.TrimPrefix(ver, "v") + "-" + goos + "-" + goarch + ext
}

// BinaryName 返回包里的可执行文件名。
func BinaryName(goos string) string {
	if goos == "windows" {
		return "lemi.exe"
	}
	return "lemi"
}

// Platform 返回「系统-架构」。
func (u *Updater) Platform() string { return u.GOOS + "-" + u.GOARCH }

// Check 查询最新正式版并缓存结果。还没有任何正式版时返回 (nil, nil)。
func (u *Updater) Check(ctx context.Context) (*Release, error) {
	rel, err := u.fetchLatest(ctx)
	u.mu.Lock()
	u.checkedAt = time.Now()
	u.checkErr = err
	if err == nil {
		u.latest = rel
	}
	u.mu.Unlock()
	return rel, err
}

// Cached 返回上次检查的结果（从未检查时 checkedAt 为零值）。
func (u *Updater) Cached() (rel *Release, checkedAt time.Time, err error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.latest, u.checkedAt, u.checkErr
}

// Newer 判断 rel 是否比当前版本新。
func (u *Updater) Newer(rel *Release) bool {
	return rel != nil && Compare(rel.Version, u.Current) > 0
}

// Applying 是否正在升级。
func (u *Updater) Applying() bool { return u.applying.Load() }

func (u *Updater) fetchLatest(ctx context.Context) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.APIURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", version.UserAgent())
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("检查新版本失败：%w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
			return nil, fmt.Errorf("检查新版本失败：GitHub 接口限流（HTTP %d），稍后再试", resp.StatusCode)
		}
		return nil, fmt.Errorf("检查新版本失败：HTTP %d", resp.StatusCode)
	}
	var list []struct {
		TagName     string    `json:"tag_name"`
		Name        string    `json:"name"`
		Body        string    `json:"body"`
		HTMLURL     string    `json:"html_url"`
		Draft       bool      `json:"draft"`
		Prerelease  bool      `json:"prerelease"`
		PublishedAt time.Time `json:"published_at"`
		Assets      []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
			Size int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAPIBody)).Decode(&list); err != nil {
		return nil, fmt.Errorf("检查新版本失败：返回内容无法解析：%w", err)
	}
	var rel *Release
	for _, body := range list {
		if body.Draft || body.Prerelease || !strings.HasPrefix(body.TagName, "v") {
			continue
		}
		v := strings.TrimPrefix(body.TagName, "v")
		if !ValidVersion(v) || (rel != nil && Compare(v, rel.Version) <= 0) {
			continue
		}
		rel = &Release{Version: v, Tag: body.TagName, Name: body.Name, Notes: body.Body, URL: body.HTMLURL, PublishedAt: body.PublishedAt}
		for _, a := range body.Assets {
			rel.Assets = append(rel.Assets, Asset{Name: a.Name, URL: a.URL, Size: a.Size})
		}
	}
	return rel, nil
}

// Executable 返回要替换的可执行文件路径（解析符号链接后的真实文件）。
func (u *Updater) Executable() (string, error) {
	p := u.ExePath
	if p == "" {
		var err error
		if p, err = os.Executable(); err != nil {
			return "", err
		}
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		p = r
	}
	return filepath.Abs(p)
}

// CanReplace 检查能否替换可执行文件（所在目录可写）。
func (u *Updater) CanReplace() error {
	exe, err := u.Executable()
	if err != nil {
		return fmt.Errorf("找不到当前可执行文件：%w", err)
	}
	f, err := os.CreateTemp(filepath.Dir(exe), ".lemi-write-test-*")
	if err != nil {
		return fmt.Errorf("没有权限写入 %s（用安装它的账号运行升级，例如 root / 管理员）", filepath.Dir(exe))
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	return nil
}

// Apply 下载、校验并替换可执行文件。成功后需要调用方安排重启（RequestRestart）。
func (u *Updater) Apply(ctx context.Context, rel *Release) (*Result, error) {
	if !u.applying.CompareAndSwap(false, true) {
		return nil, ErrBusy
	}
	defer u.applying.Store(false)
	if rel == nil {
		return nil, ErrNoRelease
	}
	if !u.Newer(rel) {
		return nil, ErrNotNewer
	}
	if !Supported(u.GOOS, u.GOARCH) {
		return nil, ErrNoPackage
	}
	exe, err := u.Executable()
	if err != nil {
		return nil, fmt.Errorf("找不到当前可执行文件：%w", err)
	}
	pkgName := PackageName(rel.Version, u.GOOS, u.GOARCH)
	pkg, sumsA, sigA := rel.asset(pkgName), rel.asset(SumsName), rel.asset(SigName)
	if sumsA == nil || sigA == nil {
		return nil, errors.New("这个版本缺少 SHA256SUMS 或 SHA256SUMS.sig，无法校验，拒绝升级")
	}
	if pkg == nil {
		return nil, fmt.Errorf("%w（%s）", ErrNoPackage, pkgName)
	}

	// 先验签名清单，再下载大文件
	sums, err := u.download(ctx, sumsA.URL, maxSumsSize)
	if err != nil {
		return nil, err
	}
	sig, err := u.download(ctx, sigA.URL, maxSigSize)
	if err != nil {
		return nil, err
	}
	if err := VerifySums(u.PublicKey, sums, sig); err != nil {
		return nil, err
	}
	list, err := ParseSums(sums)
	if err != nil {
		return nil, err
	}
	want, ok := list[pkgName]
	if !ok {
		return nil, fmt.Errorf("签名清单里没有 %s（清单与版本 %s 不符），拒绝升级", pkgName, rel.Version)
	}

	archive, err := os.CreateTemp("", "lemi-update-*.pkg")
	if err != nil {
		return nil, err
	}
	defer func() {
		archive.Close()
		os.Remove(archive.Name())
	}()
	got, err := u.downloadTo(ctx, pkg.URL, archive, maxPackageSize)
	if err != nil {
		return nil, err
	}
	if got != want {
		return nil, ErrChecksum
	}

	newFile, err := extractBinary(archive, u.GOOS == "windows", BinaryName(u.GOOS), filepath.Dir(exe))
	if err != nil {
		return nil, err
	}
	keep := false
	defer func() {
		if !keep {
			os.Remove(newFile)
		}
	}()
	if fi, err := os.Stat(exe); err == nil && u.GOOS != "windows" {
		_ = os.Chmod(newFile, fi.Mode().Perm()|0o500)
	}
	if !u.SkipExecCheck {
		if err := checkBinary(ctx, newFile, rel.Version); err != nil {
			return nil, err
		}
	}
	if err := replaceExecutable(exe, newFile); err != nil {
		return nil, fmt.Errorf("替换可执行文件失败：%w", err)
	}
	keep = true
	return &Result{From: u.Current, To: rel.Version, Exe: exe}, nil
}

func (u *Updater) get(ctx context.Context, url string) (*http.Response, error) {
	if !strings.HasPrefix(url, "https://") {
		return nil, errors.New("下载地址不是 HTTPS，已拒绝")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", version.UserAgent())
	req.Header.Set("Accept", "application/octet-stream")
	resp, err := u.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载 %s 失败：%w", path.Base(url), err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("下载 %s 失败：HTTP %d", path.Base(url), resp.StatusCode)
	}
	return resp, nil
}

func (u *Updater) download(ctx context.Context, url string, limit int64) ([]byte, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("下载 %s 失败：%w", path.Base(url), err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("%s 超过大小上限", path.Base(url))
	}
	return b, nil
}

// downloadTo 下载到文件并返回 SHA-256（小写十六进制）。
func (u *Updater) downloadTo(ctx context.Context, url string, f *os.File, limit int64) (string, error) {
	resp, err := u.get(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return "", fmt.Errorf("下载 %s 失败：%w", path.Base(url), err)
	}
	if n > limit {
		return "", fmt.Errorf("%s 超过大小上限", path.Base(url))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// extractBinary 从发布包里取出可执行文件，写到 dir 下的临时文件（与目标同一文件系统，替换时才是原子改名）。
// 只接受包根目录或唯一一层子目录下的普通文件，拒绝链接。
func extractBinary(archive *os.File, isZip bool, binName, dir string) (string, error) {
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	match := func(name string) bool {
		name = strings.TrimPrefix(path.Clean(strings.ReplaceAll(name, `\`, "/")), "./")
		if strings.HasPrefix(name, "../") || strings.HasPrefix(name, "/") {
			return false
		}
		parts := strings.Split(name, "/")
		return len(parts) <= 2 && parts[len(parts)-1] == binName
	}
	ext := ""
	if strings.HasSuffix(binName, ".exe") {
		ext = ".exe"
	}
	write := func(r io.Reader) (string, error) {
		out, err := os.CreateTemp(dir, ".lemi-update-*"+ext)
		if err != nil {
			return "", fmt.Errorf("没有权限写入 %s：%w", dir, err)
		}
		n, err := io.Copy(out, io.LimitReader(r, maxBinarySize+1))
		if err == nil && n > maxBinarySize {
			err = errors.New("可执行文件超过大小上限")
		}
		if err == nil {
			err = out.Sync()
		}
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err == nil {
			err = os.Chmod(out.Name(), 0o755)
		}
		if err != nil {
			os.Remove(out.Name())
			return "", err
		}
		return out.Name(), nil
	}
	if isZip {
		fi, err := archive.Stat()
		if err != nil {
			return "", err
		}
		zr, err := zip.NewReader(archive, fi.Size())
		if err != nil {
			return "", fmt.Errorf("发布包不是有效的 zip：%w", err)
		}
		for _, f := range zr.File {
			if !f.Mode().IsRegular() || !match(f.Name) {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return "", err
			}
			p, err := write(rc)
			rc.Close()
			return p, err
		}
		return "", errBadArchive
	}
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return "", fmt.Errorf("发布包不是有效的 tar.gz：%w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return "", errBadArchive
		}
		if err != nil {
			return "", fmt.Errorf("发布包不是有效的 tar.gz：%w", err)
		}
		if h.Typeflag != tar.TypeReg || !match(h.Name) {
			continue
		}
		return write(tr)
	}
}

// checkBinary 运行新程序的 version，确认能在本机运行且版本号对得上。
func checkBinary(ctx context.Context, file, want string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, file, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("新版本无法在本机运行：%w", err)
	}
	if !strings.Contains(string(out), "lemi "+want) {
		return fmt.Errorf("新版本报告的版本号与 %s 不符：%s", want, strings.TrimSpace(string(out)))
	}
	return nil
}

// CleanupOld 删除上次升级留下的旧可执行文件（Windows 的 lemi.exe.old）。
// 旧进程可能还没完全退出，删不掉时忽略，下次再删。
func (u *Updater) CleanupOld() {
	exe, err := u.Executable()
	if err != nil {
		return
	}
	olds, _ := filepath.Glob(exe + "*.old")
	for _, f := range olds {
		_ = os.Remove(f)
	}
	stale, _ := filepath.Glob(filepath.Join(filepath.Dir(exe), ".lemi-update-*"))
	for _, f := range stale {
		if fi, err := os.Stat(f); err == nil && time.Since(fi.ModTime()) > time.Hour {
			_ = os.Remove(f)
		}
	}
}
