package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
)

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewTOTPSecret 生成 20 字节的 TOTP 密钥（base32）。
func NewTOTPSecret() string {
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b32.EncodeToString(b)
}

// TOTPURL 生成验证器 App 扫码用的 otpauth 链接。
func TOTPURL(issuer, account, secret string) string {
	v := url.Values{}
	v.Set("secret", secret)
	v.Set("issuer", issuer)
	v.Set("algorithm", "SHA1")
	v.Set("digits", "6")
	v.Set("period", "30")
	return "otpauth://totp/" + url.PathEscape(issuer+":"+account) + "?" + v.Encode()
}

func hotp(key []byte, counter uint64) string {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	m := hmac.New(sha1.New, key)
	m.Write(buf[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := (binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff) % 1000000
	return fmt.Sprintf("%06d", code)
}

// MatchTOTP 校验 6 位验证码（允许前后各一个时间窗）。返回匹配的时间步；
// lastStep 是该用户上次用过的时间步，不大于它的一律拒绝（防重放）。
func MatchTOTP(secret, code string, lastStep int64, now time.Time) (int64, bool) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return 0, false
	}
	key, err := b32.DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return 0, false
	}
	cur := now.Unix() / 30
	for _, d := range []int64{0, -1, 1} {
		step := cur + d
		if step <= lastStep {
			continue
		}
		if hmac.Equal([]byte(hotp(key, uint64(step))), []byte(code)) {
			return step, true
		}
	}
	return 0, false
}

// CodeAt 生成某时刻的验证码（测试用）。
func CodeAt(secret string, t time.Time) string {
	key, _ := b32.DecodeString(strings.ToUpper(secret))
	return hotp(key, uint64(t.Unix()/30))
}

// NewRecoveryCodes 生成 10 个一次性恢复码，返回明文与哈希。
func NewRecoveryCodes() (plain []string, hashes []string) {
	for i := 0; i < 10; i++ {
		b := make([]byte, 8)
		if _, err := rand.Read(b); err != nil {
			panic(err)
		}
		s := strings.ToLower(b32.EncodeToString(b))[:10]
		code := s[:5] + "-" + s[5:]
		plain = append(plain, code)
		hashes = append(hashes, HashRecoveryCode(code))
	}
	return
}

// HashRecoveryCode 恢复码本身是高熵随机串，sha256 即可。
func HashRecoveryCode(code string) string {
	code = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), " ", ""))
	sum := sha256.Sum256([]byte("lemi-recovery:" + code))
	return hex.EncodeToString(sum[:])
}
