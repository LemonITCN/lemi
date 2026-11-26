package selfupdate

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// 发布签名：每个 Release 附带 SHA256SUMS（sha256sum 格式）与 SHA256SUMS.sig。
// 签名 = Ed25519(私钥, SHA256SUMS 文件的原始字节)，.sig 文件内容是 64 字节签名的标准 base64（可带换行）。
// 私钥只在 GitHub Secrets（LEMI_RELEASE_SIGNING_KEY，32 字节种子的 base64）与维护者本机 ~/.lemi-secrets/；
// 公钥编进程序（pubkey.go），在线升级只接受用它验证通过的清单。
// 不加签名域前缀，任何人都能用 openssl 手工核对（见 README「校验下载」）。

// ErrBadSignature 发布签名无效。
var ErrBadSignature = errors.New("发布签名无效：SHA256SUMS 不是 Lemi 官方签发的，拒绝升级")

// ReleasePublicKey 返回编进程序的发布签名公钥。
func ReleasePublicKey() ed25519.PublicKey {
	b, err := base64.StdEncoding.DecodeString(releasePublicKeyB64)
	if err != nil || len(b) != ed25519.PublicKeySize {
		panic("selfupdate: 内置发布公钥格式错误")
	}
	return ed25519.PublicKey(b)
}

// ParseSigningKey 解析 32 字节种子的 base64（LEMI_RELEASE_SIGNING_KEY 的格式）。
func ParseSigningKey(b64 string) (ed25519.PrivateKey, error) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(b64))
	if err != nil {
		return nil, fmt.Errorf("签名私钥不是合法的 base64：%w", err)
	}
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("签名私钥应为 %d 字节种子，实际 %d 字节", ed25519.SeedSize, len(seed))
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// SignSums 对 SHA256SUMS 内容签名，返回 .sig 文件内容。
func SignSums(key ed25519.PrivateKey, sums []byte) []byte {
	return []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, sums)) + "\n")
}

// VerifySums 用公钥验证 SHA256SUMS 与 .sig 文件内容。
func VerifySums(pub ed25519.PublicKey, sums, sigFile []byte) error {
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sigFile)))
	if err != nil || len(sig) != ed25519.SignatureSize || len(pub) != ed25519.PublicKeySize {
		return ErrBadSignature
	}
	if !ed25519.Verify(pub, sums, sig) {
		return ErrBadSignature
	}
	return nil
}

// ParseSums 解析 sha256sum 格式的清单：每行「64 位十六进制  文件名」（文件名前可带 * 表示二进制模式）。
// 文件名不能含路径分隔符；同名重复或格式不对的行直接报错，避免清单被做手脚时含糊处理。
func ParseSums(sums []byte) (map[string]string, error) {
	out := map[string]string{}
	sc := bufio.NewScanner(bytes.NewReader(sums))
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) < 66 || line[64] != ' ' {
			return nil, fmt.Errorf("SHA256SUMS 第 %d 行格式不正确", n)
		}
		sum := strings.ToLower(line[:64])
		if _, err := hex.DecodeString(sum); err != nil {
			return nil, fmt.Errorf("SHA256SUMS 第 %d 行哈希不正确", n)
		}
		name := strings.TrimPrefix(strings.TrimLeft(line[65:], " "), "*")
		if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
			return nil, fmt.Errorf("SHA256SUMS 第 %d 行文件名不正确", n)
		}
		if _, dup := out[name]; dup {
			return nil, fmt.Errorf("SHA256SUMS 里 %s 出现了两次", name)
		}
		out[name] = sum
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
