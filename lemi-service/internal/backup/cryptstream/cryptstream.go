// Package cryptstream 是备份文件的流式加密格式 .lemienc（docs/spec.md §12.3）。
//
// 文件布局（多字节整数一律大端）：
//
//	头部 58 字节：
//	  魔数 "LEMIENC1"（8）
//	  KDF 编号（1）= 1，表示 argon2id
//	  argon2id 迭代次数 t（4）、内存 m（4，单位 KiB）、并行度 p（1）
//	  盐长度（1）= 16，盐（16）
//	  随机 nonce 前缀（7）
//	  口令校验值（16）= HMAC-SHA256(密钥, "lemi-backup-key-check") 的前 16 字节
//	之后是若干块：每块明文 1 MiB（最后一块可更短、可为空），AES-256-GCM 加密，密文 = 明文 + 16 字节认证标签。
//
// 每块的 12 字节 nonce = 7 字节前缀 + 4 字节块序号 + 1 字节「是否最后一块」（0/1）；附加数据（AAD）是整个头部。
//   - 块被替换、重排：序号进 nonce，认证失败；
//   - 截断在块边界：最后读到的那块是按「非最后一块」加密的，按最后一块解密认证失败；
//   - 尾部追加数据、改动头部（KDF 参数、盐）：认证失败；
//   - 口令不对：先由口令校验值发现，明确报「备份密码不正确」。
//
// 除最后一块外每块密文长度固定，解密端不需要长度字段就能切块。空明文也会写出一个空的最后一块。
// 密钥只由备份密码派生（不依赖 Lemi 主密钥），丢了 Lemi 数据目录也能用 `lemi backup-file` 加备份密码解开。
package cryptstream

import (
	"bufio"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"io"

	"golang.org/x/crypto/argon2"
)

const (
	// Magic 是文件开头的魔数。
	Magic = "LEMIENC1"
	// Ext 是加密备份的扩展名。
	Ext = ".lemienc"
	// ChunkSize 是每块明文大小。
	ChunkSize = 1 << 20
	// HeaderSize 是头部长度。
	HeaderSize = 58

	kdfArgon2id  = 1
	saltSize     = 16
	prefixSize   = 7
	checkSize    = 16
	tagSize      = 16
	sealedChunk  = ChunkSize + tagSize
	maxChunks    = 1 << 32
	keySize      = 32
	keyCheckInfo = "lemi-backup-key-check"

	// 解密时接受的 KDF 参数上限，防止恶意文件用超大参数耗尽内存或 CPU。
	maxTime      = 16
	maxMemoryKiB = 1 << 20 // 1 GiB
	maxThreads   = 64
)

// Params 是 argon2id 参数。
type Params struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

// DefaultParams 是加密时使用的参数（与登录口令哈希同档：t=3、m=64 MiB、p=2）。测试可以调低。
var DefaultParams = Params{Time: 3, MemoryKiB: 64 * 1024, Threads: 2}

var (
	// ErrNotEncrypted 不是 .lemienc 文件。
	ErrNotEncrypted = errors.New("不是 Lemi 加密备份文件（.lemienc）")
	// ErrUnsupported 格式参数不认识（可能是更新版本的 Lemi 写的）。
	ErrUnsupported = errors.New("加密备份文件的格式不受支持，请升级 Lemi")
	// ErrBadPassphrase 备份密码不正确。
	ErrBadPassphrase = errors.New("备份密码不正确")
	// ErrCorrupt 认证失败：文件损坏、被篡改或不完整。
	ErrCorrupt = errors.New("解密失败：备份文件已损坏、被篡改或不完整")
	// ErrTooLarge 超过格式上限（2^32 块）。
	ErrTooLarge = errors.New("文件太大，超出加密格式上限")
	// ErrClosed 对已关闭的写入器继续写入。
	ErrClosed = errors.New("加密写入器已关闭")
	// ErrEmptyPassphrase 没有备份密码。
	ErrEmptyPassphrase = errors.New("备份密码不能为空")
)

// IsEncrypted 判断数据开头是否是 .lemienc 魔数。
func IsEncrypted(head []byte) bool {
	return len(head) >= len(Magic) && string(head[:len(Magic)]) == Magic
}

func deriveKey(passphrase string, salt []byte, p Params) []byte {
	return argon2.IDKey([]byte(passphrase), salt, p.Time, p.MemoryKiB, p.Threads, keySize)
}

func keyCheck(key []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(keyCheckInfo))
	return m.Sum(nil)[:checkSize]
}

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// header 字段偏移。
const (
	offKDF    = 8
	offTime   = 9
	offMem    = 13
	offThread = 17
	offSaltN  = 18
	offSalt   = 19
	offPrefix = offSalt + saltSize
	offCheck  = offPrefix + prefixSize
)

func chunkNonce(nonce *[12]byte, prefix []byte, seq uint64, last bool) {
	copy(nonce[:prefixSize], prefix)
	binary.BigEndian.PutUint32(nonce[prefixSize:prefixSize+4], uint32(seq))
	if last {
		nonce[11] = 1
	} else {
		nonce[11] = 0
	}
}

// ---------------- 加密 ----------------

type writer struct {
	w      io.Writer
	aead   cipher.AEAD
	header [HeaderSize]byte
	wrote  bool
	buf    []byte
	out    []byte
	seq    uint64
	nonce  [12]byte
	err    error
	closed bool
}

// NewWriter 返回加密写入器：写入的明文加密后写到 w。必须调用 Close 才会写出最后一块（没有 Close 的密文会被判为截断）。
// Close 不会关闭 w。
func NewWriter(w io.Writer, passphrase string) (io.WriteCloser, error) {
	return NewWriterParams(w, passphrase, DefaultParams)
}

// NewWriterParams 用指定的 KDF 参数加密。
func NewWriterParams(w io.Writer, passphrase string, p Params) (io.WriteCloser, error) {
	if passphrase == "" {
		return nil, ErrEmptyPassphrase
	}
	if p.Time == 0 || p.MemoryKiB == 0 || p.Threads == 0 {
		return nil, errors.New("KDF 参数不正确")
	}
	e := &writer{w: w, buf: make([]byte, 0, ChunkSize), out: make([]byte, 0, sealedChunk)}
	h := e.header[:]
	copy(h, Magic)
	h[offKDF] = kdfArgon2id
	binary.BigEndian.PutUint32(h[offTime:], p.Time)
	binary.BigEndian.PutUint32(h[offMem:], p.MemoryKiB)
	h[offThread] = p.Threads
	h[offSaltN] = saltSize
	if _, err := rand.Read(h[offSalt : offSalt+saltSize]); err != nil {
		return nil, err
	}
	if _, err := rand.Read(h[offPrefix : offPrefix+prefixSize]); err != nil {
		return nil, err
	}
	key := deriveKey(passphrase, h[offSalt:offSalt+saltSize], p)
	copy(h[offCheck:], keyCheck(key))
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	e.aead = aead
	return e, nil
}

func (e *writer) writeHeader() error {
	if e.wrote {
		return nil
	}
	if _, err := e.w.Write(e.header[:]); err != nil {
		return err
	}
	e.wrote = true
	return nil
}

func (e *writer) seal(last bool) error {
	if e.seq >= maxChunks {
		return ErrTooLarge
	}
	chunkNonce(&e.nonce, e.header[offPrefix:offPrefix+prefixSize], e.seq, last)
	e.out = e.aead.Seal(e.out[:0], e.nonce[:], e.buf, e.header[:])
	if _, err := e.w.Write(e.out); err != nil {
		return err
	}
	e.seq++
	e.buf = e.buf[:0]
	return nil
}

func (e *writer) Write(p []byte) (int, error) {
	if e.closed {
		return 0, ErrClosed
	}
	if e.err != nil {
		return 0, e.err
	}
	if err := e.writeHeader(); err != nil {
		e.err = err
		return 0, err
	}
	n := 0
	for len(p) > 0 {
		// 缓冲区满了而且还有数据，才能确定它不是最后一块。
		if len(e.buf) == ChunkSize {
			if err := e.seal(false); err != nil {
				e.err = err
				return n, err
			}
		}
		k := copy(e.buf[len(e.buf):ChunkSize], p)
		e.buf = e.buf[:len(e.buf)+k]
		p = p[k:]
		n += k
	}
	return n, nil
}

// Close 写出最后一块（可能为空）。重复调用返回第一次的结果。
func (e *writer) Close() error {
	if e.closed {
		return e.err
	}
	e.closed = true
	if e.err != nil {
		return e.err
	}
	if err := e.writeHeader(); err != nil {
		e.err = err
		return err
	}
	if err := e.seal(true); err != nil {
		e.err = err
		return err
	}
	return nil
}

// ---------------- 解密 ----------------

type reader struct {
	r      *bufio.Reader
	aead   cipher.AEAD
	header [HeaderSize]byte
	in     []byte
	plain  []byte
	seq    uint64
	nonce  [12]byte
	done   bool
	err    error
}

// ReadHeader 读取并校验头部，返回 KDF 参数（不派生密钥）。
func ReadHeader(r io.Reader) ([HeaderSize]byte, Params, error) {
	var h [HeaderSize]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return h, Params{}, ErrNotEncrypted
		}
		return h, Params{}, err
	}
	if !IsEncrypted(h[:]) {
		return h, Params{}, ErrNotEncrypted
	}
	if h[offKDF] != kdfArgon2id || h[offSaltN] != saltSize {
		return h, Params{}, ErrUnsupported
	}
	p := Params{Time: binary.BigEndian.Uint32(h[offTime:]), MemoryKiB: binary.BigEndian.Uint32(h[offMem:]), Threads: h[offThread]}
	if p.Time == 0 || p.Time > maxTime || p.MemoryKiB < 8*uint32(p.Threads) || p.MemoryKiB > maxMemoryKiB || p.Threads == 0 || p.Threads > maxThreads {
		return h, Params{}, ErrUnsupported
	}
	return h, p, nil
}

// NewReader 读取头部、用备份密码派生密钥并校验，返回明文读取器。
//
// 解密逐块进行，每块先认证再交出明文；但截断这类问题只有读到末尾才能发现。所以只有 Read 最终返回 io.EOF 时
// 读到的数据才是完整的；中途返回其他错误时，调用方必须丢弃已经读到的数据（例如还原时回滚或删除临时库）。
func NewReader(r io.Reader, passphrase string) (io.Reader, error) {
	if passphrase == "" {
		return nil, ErrEmptyPassphrase
	}
	br := bufio.NewReaderSize(r, sealedChunk+1)
	h, p, err := ReadHeader(br)
	if err != nil {
		return nil, err
	}
	key := deriveKey(passphrase, h[offSalt:offSalt+saltSize], p)
	if !hmac.Equal(keyCheck(key), h[offCheck:offCheck+checkSize]) {
		return nil, ErrBadPassphrase
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &reader{r: br, aead: aead, header: h, in: make([]byte, sealedChunk)}, nil
}

func (d *reader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for len(d.plain) == 0 {
		if d.err != nil {
			return 0, d.err
		}
		if d.done {
			return 0, io.EOF
		}
		if err := d.next(); err != nil {
			d.err = err
			return 0, err
		}
	}
	n := copy(p, d.plain)
	d.plain = d.plain[n:]
	return n, nil
}

func (d *reader) next() error {
	n, err := io.ReadFull(d.r, d.in)
	var last bool
	switch {
	case err == nil:
		// 读满一整块：后面没有数据了它才是最后一块。
		if _, perr := d.r.Peek(1); perr == io.EOF {
			last = true
		} else if perr != nil {
			return perr
		}
	case err == io.ErrUnexpectedEOF:
		last = true
	case err == io.EOF:
		return ErrCorrupt // 上一块不是最后一块，数据却没了
	default:
		return err
	}
	if n < tagSize || d.seq >= maxChunks {
		return ErrCorrupt
	}
	chunkNonce(&d.nonce, d.header[offPrefix:offPrefix+prefixSize], d.seq, last)
	plain, oerr := d.aead.Open(d.in[:0], d.nonce[:], d.in[:n], d.header[:])
	if oerr != nil {
		return ErrCorrupt
	}
	d.seq++
	d.plain = plain
	d.done = last
	return nil
}

// EncryptedSize 返回 plain 字节明文加密后的精确长度。
func EncryptedSize(plain int64) int64 {
	chunks := (plain + ChunkSize - 1) / ChunkSize
	if chunks == 0 {
		chunks = 1
	}
	return HeaderSize + plain + chunks*tagSize
}
