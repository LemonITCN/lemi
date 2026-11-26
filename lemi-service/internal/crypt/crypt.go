// Package crypt 管理主密钥并做字段级加密：连接密码、云存储密钥、模型密钥、两步验证密钥等。
//
// 主密钥 32 字节，来自环境变量 LEMI_MASTER_KEY（base64）或 <数据目录>/master.key（首次启动生成，0600）。
// 各用途的子密钥用 HKDF-SHA256 派生；每个密文都绑定附加数据（表名/记录 ID/字段名），密文不能挪到别的记录上解开。
package crypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	prefix  = "enc:v1:"
	keySize = 32
)

// LoadOrCreateKey 读取主密钥；envKey 非空时用它（不写文件），否则读 path，不存在则生成。
func LoadOrCreateKey(envKey, path string) ([]byte, bool, error) {
	if envKey != "" {
		k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(envKey))
		if err != nil || len(k) != keySize {
			return nil, false, errors.New("LEMI_MASTER_KEY 必须是 32 字节的 base64")
		}
		return k, false, nil
	}
	b, err := os.ReadFile(path)
	if err == nil {
		k, derr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
		if derr != nil || len(k) != keySize {
			return nil, false, fmt.Errorf("主密钥文件 %s 内容不正确", path)
		}
		return k, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, false, err
	}
	k := make([]byte, keySize)
	if _, err := rand.Read(k); err != nil {
		return nil, false, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, false, err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(base64.StdEncoding.EncodeToString(k)+"\n"), 0o600); err != nil {
		return nil, false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return nil, false, err
	}
	return k, true, nil
}

// Box 用主密钥派生的子密钥做 AES-256-GCM 加解密。
type Box struct {
	master []byte
	aead   cipher.AEAD
}

// New 用主密钥创建 Box。
func New(master []byte) (*Box, error) {
	if len(master) != keySize {
		return nil, errors.New("crypt: 主密钥必须是 32 字节")
	}
	k, err := Derive(master, "lemi/field/v1")
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{master: master, aead: aead}, nil
}

// Derive 用 HKDF-SHA256 从主密钥派生 32 字节子密钥。
func Derive(master []byte, info string) ([]byte, error) {
	return hkdf.Key(sha256.New, master, nil, info, keySize)
}

// SubKey 派生指定用途的子密钥（例如会话签名、下载令牌）。
func (b *Box) SubKey(info string) []byte {
	k, err := Derive(b.master, info)
	if err != nil {
		panic(err)
	}
	return k
}

// AAD 生成附加数据。
func AAD(table, id, field string) string { return table + "\x00" + id + "\x00" + field }

// Seal 加密；空串返回空串。aad 绑定密文所属位置。
func (b *Box) Seal(plain, aad string) string {
	if plain == "" {
		return ""
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	ct := b.aead.Seal(nonce, nonce, []byte(plain), []byte(aad))
	return prefix + base64.RawStdEncoding.EncodeToString(ct)
}

// ErrDecrypt 解密失败。
var ErrDecrypt = errors.New("解密失败：主密钥不匹配或数据被篡改")

// Open 解密；空串返回空串。
func (b *Box) Open(s, aad string) (string, error) {
	if s == "" {
		return "", nil
	}
	if !strings.HasPrefix(s, prefix) {
		return "", ErrDecrypt
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(s, prefix))
	if err != nil {
		return "", ErrDecrypt
	}
	ns := b.aead.NonceSize()
	if len(raw) < ns+b.aead.Overhead() {
		return "", ErrDecrypt
	}
	pt, err := b.aead.Open(nil, raw[:ns], raw[ns:], []byte(aad))
	if err != nil {
		return "", ErrDecrypt
	}
	return string(pt), nil
}

// SealJSON 加密一个 JSON 对象里的若干字段：secrets 中的键加密后写回。
// 用于驱动配置、存储目标配置等「部分字段是密钥」的场景。
func (b *Box) SealFields(m map[string]any, keys []string, aadBase string) {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" && !strings.HasPrefix(v, prefix) {
			m[k] = b.Seal(v, aadBase+"\x00"+k)
		}
	}
}

// OpenFields 解密 SealFields 加密过的字段。
func (b *Box) OpenFields(m map[string]any, keys []string, aadBase string) error {
	for _, k := range keys {
		if v, ok := m[k].(string); ok && v != "" {
			p, err := b.Open(v, aadBase+"\x00"+k)
			if err != nil {
				return fmt.Errorf("字段 %s：%w", k, err)
			}
			m[k] = p
		}
	}
	return nil
}

// IsSealed 判断字符串是否是密文。
func IsSealed(s string) bool { return strings.HasPrefix(s, prefix) }
