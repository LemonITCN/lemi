// Package auth 负责口令、两步验证、会话、登录限速与二次验证（re-auth）。
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

const (
	argonTime    = 3
	argonMemory  = 64 * 1024
	argonThreads = 2
	argonKeyLen  = 32
)

// MinPasswordLen 口令最短长度。
const MinPasswordLen = 10

// HashPassword 用 argon2id 生成口令哈希。
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// argonSem 限制同时进行的 argon2 计算数，防止大量登录请求耗尽内存。
var argonSem = make(chan struct{}, 4)

// VerifyPassword 校验口令（常量时间比较）。
func VerifyPassword(pw, encoded string) bool {
	argonSem <- struct{}{}
	defer func() { <-argonSem }()
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	if m > 256*1024 || t > 10 || p > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// DummyVerify 用户不存在时也做一次等价计算，避免按耗时探测用户名。
func DummyVerify(pw string) { VerifyPassword(pw, dummyHash()) }

var (
	dummyOnce sync.Once
	dummy     string
)

func dummyHash() string {
	dummyOnce.Do(func() { dummy, _ = HashPassword("lemi-dummy-password-for-timing") })
	return dummy
}

//go:embed weak_passwords.txt
var weakList string

var weakSet = func() map[string]bool {
	m := map[string]bool{}
	for _, l := range strings.Split(weakList, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			m[l] = true
		}
	}
	return m
}()

// CheckPasswordPolicy 校验口令强度：至少 10 位、不能是常见弱口令、不能与用户名相同、不能全是同一字符。
func CheckPasswordPolicy(pw, username string) error {
	n := utf8.RuneCountInString(pw)
	if n < MinPasswordLen {
		return fmt.Errorf("口令至少 %d 位", MinPasswordLen)
	}
	if len(pw) > 256 {
		return errors.New("口令太长")
	}
	lower := strings.ToLower(pw)
	if username != "" && strings.Contains(lower, strings.ToLower(username)) && n < len(username)+6 {
		return errors.New("口令不能与用户名过于相似")
	}
	if weakSet[lower] {
		return errors.New("这是常见的弱口令，请换一个")
	}
	first, _ := utf8.DecodeRuneInString(pw)
	same := true
	classes := map[string]bool{}
	for _, r := range pw {
		if r != first {
			same = false
		}
		switch {
		case unicode.IsDigit(r):
			classes["d"] = true
		case unicode.IsLetter(r):
			classes["l"] = true
		default:
			classes["s"] = true
		}
	}
	if same {
		return errors.New("口令不能全是同一个字符")
	}
	if len(classes) < 2 && n < 16 {
		return errors.New("口令至少要包含字母、数字、符号中的两类（或不少于 16 位）")
	}
	return nil
}
