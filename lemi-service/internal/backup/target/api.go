// Package target 实现备份存储目标：本地目录/NAS、SFTP、WebDAV、FTP/FTPS、SMB、S3 兼容（AWS/R2/MinIO）、
// 腾讯云 COS、阿里云 OSS、Azure Blob、百度网盘（docs/spec.md §12.7）。备份引擎只通过 Target 接口读写目标。
//
// 本文件是对外契约（备份引擎依赖它），各后端实现放在各自的文件里。
//
// 路径约定：
//   - 接口里的 path 一律是相对目标根目录（含配置里的前缀/根目录）的相对路径，用 / 分隔；
//   - 拒绝空路径、绝对路径、反斜杠、冒号、控制字符、空段（a//b）、. 与 .. 段、结尾的 /；
//   - 以 ~$ 开头的文件名留给本包写临时文件，Put 会拒绝，List 会跳过。
package target

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// ErrNotExist 表示目标上没有这个文件。
var ErrNotExist = errors.New("文件不存在")

// ErrSizeMismatch 表示 Put 时数据长度与声明的 size 不一致。
var ErrSizeMismatch = errors.New("数据长度与声明的大小不一致")

// Entry 是目标上的一个文件。
type Entry struct {
	Path    string    `json:"path"` // 相对目标根目录，用 / 分隔
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
}

// Target 是一个备份目标。所有方法都可以并发调用。
type Target interface {
	// Put 写入完整文件。size >= 0 时 r 必须恰好读出 size 字节；size < 0 表示大小未知，读到 EOF 为止（流式）。
	// r 返回非 EOF 的错误时必须放弃本次写入，不能以正式文件名留下半个文件。
	// 支持的后端先写临时名再改名，或用分片上传最后合并，保证不会留下半个文件冒充成品。
	Put(ctx context.Context, path string, r io.Reader, size int64) error
	// Get 读取文件；不存在返回 ErrNotExist（可用 errors.Is 判断）。读到的数据长度不对时 Read 返回错误。
	Get(ctx context.Context, path string) (io.ReadCloser, error)
	// List 递归列出路径以 prefix 开头的文件（不含目录），按字符串前缀匹配；prefix 为空列出全部。
	// 目录不存在时返回空列表。
	List(ctx context.Context, prefix string) ([]Entry, error)
	// Delete 删除文件，不存在视为成功。
	Delete(ctx context.Context, path string) error
	// Stat 查询文件；不存在返回 ErrNotExist。
	Stat(ctx context.Context, path string) (Entry, error)
	Close() error
}

// Deps 是打开目标时需要的外部依赖。
type Deps struct {
	// HTTPClient 用于 HTTP 类目标（S3/COS/OSS/WebDAV/Azure/百度网盘）；出站地址检查（内网拦截）由调用方在它的
	// Transport 里做。为 nil 时使用内置的默认客户端（只用于测试）。
	HTTPClient *http.Client
	// Dial 用于 TCP 类目标（SFTP/FTP/SMB）建立连接；出站地址检查由调用方做。为 nil 时直接拨号（只用于测试）。
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// SaveConfig 在百度网盘刷新令牌后回写新配置（明文 JSON，保留原配置里本包不认识的字段）。
	SaveConfig func(newConfigJSON []byte) error
	// SpoolDir 是必须预先知道文件大小的后端（百度网盘，以及勾选了「先暂存」的 WebDAV）在流式写入时
	// 暂存数据的本机目录。为空时用系统临时目录。
	SpoolDir string
	// LocalRoot 是本地目录类目标在配置里没有填路径时使用的默认根目录（<数据目录>/backups）。
	LocalRoot string
}

// 目标类型。
const (
	KindLocal  = "local"
	KindSFTP   = "sftp"
	KindWebDAV = "webdav"
	KindFTP    = "ftp"
	KindSMB    = "smb"
	KindS3     = "s3"
	KindCOS    = "cos"
	KindOSS    = "oss"
	KindAzure  = "azure"
	KindBaidu  = "baidu"
)

// Field 是目标配置表单的一个字段（前端按它渲染）。
type Field struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	LabelEN     string   `json:"label_en,omitempty"`
	Type        string   `json:"type"` // text | password | number | bool | select | textarea
	Required    bool     `json:"required,omitempty"`
	Secret      bool     `json:"secret,omitempty"` // 加密保存、接口只写不读
	Default     any      `json:"default,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// KindInfo 描述一种目标。
type KindInfo struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	NameEN string  `json:"name_en"`
	Fields []Field `json:"fields"`
	// AdminOnly 只有实例管理员可以创建或修改（本地目录：路径指向 Lemi 主机的文件系统）。
	AdminOnly bool `json:"admin_only,omitempty"`
	// Network 是否经网络访问（出站地址受「允许访问内网」设置约束）。
	Network bool `json:"network"`
	// OAuth 需要扫码授权（百度网盘）。
	OAuth bool `json:"oauth,omitempty"`
}

// SecretKeys 返回需要加密保存的配置键。
func (k KindInfo) SecretKeys() []string {
	var out []string
	for _, f := range k.Fields {
		if f.Secret {
			out = append(out, f.Key)
		}
	}
	return out
}

// ---------------- 流式上传 ----------------

// Upload 是一个进行中的流式写入：数据经 Write 写入，Commit 结束并等待后端确认，Abort 放弃（不留下文件）。
// Write 与 Commit/Abort 不能并发调用。
type Upload struct {
	pw     *io.PipeWriter
	done   chan struct{}
	err    error
	n      int64
	once   sync.Once
	closed bool
}

// NewUpload 开始向 t 流式写入 path（大小未知）。后端在后台读取，写入速度受后端速度限制。
func NewUpload(ctx context.Context, t Target, path string) *Upload {
	pr, pw := io.Pipe()
	u := &Upload{pw: pw, done: make(chan struct{})}
	go func() {
		defer close(u.done)
		err := t.Put(ctx, path, pr, -1)
		if err == nil {
			// 后端声称成功，但数据可能没被读完（实现错误）：剩余数据读不出来就算失败。
			if _, rerr := pr.Read(make([]byte, 1)); rerr != io.EOF {
				err = errors.New("备份目标没有读完全部数据")
			}
		}
		u.err = err
		pr.CloseWithError(errUploadFinished(err))
	}()
	return u
}

type uploadFinished struct{ err error }

func (e uploadFinished) Error() string {
	if e.err != nil {
		return e.err.Error()
	}
	return "上传已结束"
}

func (e uploadFinished) Unwrap() error { return e.err }

func errUploadFinished(err error) error {
	if err == nil {
		return uploadFinished{}
	}
	return err
}

// Write 写入数据。后端已经失败时返回其错误。
func (u *Upload) Write(p []byte) (int, error) {
	n, err := u.pw.Write(p)
	u.n += int64(n)
	if err != nil {
		// 后端提前结束：等它给出真正的错误。
		<-u.done
		if u.err != nil {
			return n, u.err
		}
		return n, err
	}
	return n, nil
}

// Written 已写入的字节数。
func (u *Upload) Written() int64 { return u.n }

// Commit 结束写入并等待后端完成；返回 nil 才表示文件已完整保存。
func (u *Upload) Commit() error {
	u.once.Do(func() { u.pw.Close() })
	<-u.done
	return u.err
}

// Abort 放弃写入，等待后端清理（临时文件、未完成的分片）。
func (u *Upload) Abort(cause error) {
	if cause == nil {
		cause = errors.New("上传已取消")
	}
	u.once.Do(func() { u.pw.CloseWithError(cause) })
	<-u.done
}

// Err 后端的结果（Commit/Abort 之后才有意义）。
func (u *Upload) Err() error {
	select {
	case <-u.done:
		return u.err
	default:
		return nil
	}
}
