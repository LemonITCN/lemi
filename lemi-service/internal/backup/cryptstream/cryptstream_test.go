package cryptstream

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"testing"
	"testing/iotest"
)

func TestMain(m *testing.M) {
	DefaultParams = Params{Time: 1, MemoryKiB: 64, Threads: 1} // 测试用低参数
	os.Exit(m.Run())
}

const pass = "测试用备份密码-not-a-real-secret"

func randBytes(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

// encrypt 用不规则的写入长度加密，覆盖跨块边界的写入。
func encrypt(t *testing.T, plain []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w, err := NewWriter(&out, pass)
	if err != nil {
		t.Fatal(err)
	}
	steps := []int{1, 7, 1000, ChunkSize - 3, ChunkSize, 3 * ChunkSize, 12345}
	p := plain
	for i := 0; len(p) > 0; i++ {
		k := steps[i%len(steps)]
		if k > len(p) {
			k = len(p)
		}
		n, err := w.Write(p[:k])
		if err != nil || n != k {
			t.Fatalf("Write: n=%d err=%v", n, err)
		}
		p = p[k:]
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func decryptAll(ct []byte, pw string) ([]byte, error) {
	r, err := NewReader(bytes.NewReader(ct), pw)
	if err != nil {
		return nil, err
	}
	return io.ReadAll(r)
}

var sizes = []int{0, 1, 100, ChunkSize - 1, ChunkSize, ChunkSize + 1, 2 * ChunkSize, 2*ChunkSize + 17, 3*ChunkSize + 5}

func TestRoundTrip(t *testing.T) {
	for _, n := range sizes {
		plain := randBytes(t, n)
		ct := encrypt(t, plain)
		if int64(len(ct)) != EncryptedSize(int64(n)) {
			t.Fatalf("size %d：密文 %d 字节，EncryptedSize 给出 %d", n, len(ct), EncryptedSize(int64(n)))
		}
		if !IsEncrypted(ct) {
			t.Fatal("魔数不对")
		}
		got, err := decryptAll(ct, pass)
		if err != nil {
			t.Fatalf("size %d：解密失败 %v", n, err)
		}
		if !bytes.Equal(got, plain) {
			t.Fatalf("size %d：往返不一致", n)
		}
		// 逐字节读也要正确
		r, _ := NewReader(iotest.OneByteReader(bytes.NewReader(ct)), pass)
		got, err = io.ReadAll(iotest.OneByteReader(r))
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("size %d：逐字节读取失败 %v", n, err)
		}
	}
}

func TestSamePlainDifferentCiphertext(t *testing.T) {
	plain := []byte("同样的内容")
	a, b := encrypt(t, plain), encrypt(t, plain)
	if bytes.Equal(a, b) {
		t.Fatal("两次加密结果相同（盐与 nonce 应随机）")
	}
}

func TestWrongPassphrase(t *testing.T) {
	ct := encrypt(t, []byte("hello"))
	if _, err := decryptAll(ct, "wrong-passphrase"); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("口令错误应报 ErrBadPassphrase，实际 %v", err)
	}
	if _, err := NewReader(bytes.NewReader(ct), ""); !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("空口令：%v", err)
	}
	if _, err := NewWriter(io.Discard, ""); !errors.Is(err, ErrEmptyPassphrase) {
		t.Fatalf("空口令加密：%v", err)
	}
}

func TestNotEncrypted(t *testing.T) {
	for _, in := range [][]byte{nil, []byte("LEMI"), []byte("-- Lemi Dump SQL\nSELECT 1;" + string(make([]byte, 80)))} {
		if _, err := decryptAll(in, pass); !errors.Is(err, ErrNotEncrypted) {
			t.Fatalf("%q：应报 ErrNotEncrypted，实际 %v", in, err)
		}
	}
}

func TestTruncation(t *testing.T) {
	plain := randBytes(t, 3*ChunkSize+100)
	ct := encrypt(t, plain)
	sealed := ChunkSize + tagSize
	cuts := []int{HeaderSize, HeaderSize + 1, HeaderSize + sealed, HeaderSize + sealed + 1, HeaderSize + 2*sealed, HeaderSize + 3*sealed, len(ct) - 1, len(ct) - tagSize}
	for _, c := range cuts {
		_, err := decryptAll(ct[:c], pass)
		if !errors.Is(err, ErrCorrupt) {
			t.Fatalf("截断到 %d 字节应报 ErrCorrupt，实际 %v", c, err)
		}
	}
	// 截断在头部内
	if _, err := decryptAll(ct[:HeaderSize-1], pass); !errors.Is(err, ErrNotEncrypted) {
		t.Fatalf("头部截断：%v", err)
	}
}

func TestTamper(t *testing.T) {
	plain := randBytes(t, 2*ChunkSize+10)
	ct := encrypt(t, plain)
	// 改动密文与标签里的任意位置
	for _, pos := range []int{HeaderSize, HeaderSize + 5000, HeaderSize + ChunkSize + tagSize + 1, len(ct) - 1} {
		bad := append([]byte(nil), ct...)
		bad[pos] ^= 0x01
		if _, err := decryptAll(bad, pass); !errors.Is(err, ErrCorrupt) {
			t.Fatalf("改动第 %d 字节应报 ErrCorrupt，实际 %v", pos, err)
		}
	}
	// 改动 nonce 前缀（头部进 AAD）
	bad := append([]byte(nil), ct...)
	bad[offPrefix] ^= 0x01
	if _, err := decryptAll(bad, pass); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("改动 nonce 前缀应报 ErrCorrupt，实际 %v", err)
	}
	// 改动盐：派生出别的密钥，口令校验不过
	bad = append([]byte(nil), ct...)
	bad[offSalt] ^= 0x01
	if _, err := decryptAll(bad, pass); !errors.Is(err, ErrBadPassphrase) {
		t.Fatalf("改动盐：%v", err)
	}
	// 追加数据
	if _, err := decryptAll(append(append([]byte(nil), ct...), 0), pass); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("追加 1 字节应报 ErrCorrupt，实际 %v", err)
	}
	// 交换两块
	sealed := ChunkSize + tagSize
	sw := append([]byte(nil), ct...)
	copy(sw[HeaderSize:HeaderSize+sealed], ct[HeaderSize+sealed:HeaderSize+2*sealed])
	copy(sw[HeaderSize+sealed:HeaderSize+2*sealed], ct[HeaderSize:HeaderSize+sealed])
	if _, err := decryptAll(sw, pass); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("交换两块应报 ErrCorrupt，实际 %v", err)
	}
	// 删掉中间一整块
	cut := append(append([]byte(nil), ct[:HeaderSize+sealed]...), ct[HeaderSize+2*sealed:]...)
	if _, err := decryptAll(cut, pass); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("删掉一块应报 ErrCorrupt，实际 %v", err)
	}
}

func TestSpliceBetweenFiles(t *testing.T) {
	plain := randBytes(t, 2*ChunkSize)
	a, b := encrypt(t, plain), encrypt(t, plain)
	// 同一口令的两份文件：把 b 的第一块换到 a 里
	sealed := ChunkSize + tagSize
	mix := append([]byte(nil), a...)
	copy(mix[HeaderSize:HeaderSize+sealed], b[HeaderSize:HeaderSize+sealed])
	if _, err := decryptAll(mix, pass); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("跨文件换块应报 ErrCorrupt，实际 %v", err)
	}
}

func TestHostileParams(t *testing.T) {
	ct := encrypt(t, []byte("x"))
	bad := append([]byte(nil), ct...)
	binary.BigEndian.PutUint32(bad[offMem:], 1<<30) // 1 TiB
	if _, err := decryptAll(bad, pass); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("超大内存参数应被拒绝，实际 %v", err)
	}
	bad = append([]byte(nil), ct...)
	binary.BigEndian.PutUint32(bad[offTime:], 1000)
	if _, err := decryptAll(bad, pass); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("超大迭代次数应被拒绝，实际 %v", err)
	}
	bad = append([]byte(nil), ct...)
	bad[offKDF] = 9
	if _, err := decryptAll(bad, pass); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("未知 KDF 应被拒绝，实际 %v", err)
	}
}

func TestWriteAfterClose(t *testing.T) {
	w, _ := NewWriter(io.Discard, pass)
	w.Close()
	if _, err := w.Write([]byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("关闭后写入：%v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("重复关闭：%v", err)
	}
}
