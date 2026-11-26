package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSecretEnvFromFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(f, []byte("s3cret pass\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEMI_SMTP_PASSWORD", "")
	os.Unsetenv("LEMI_SMTP_PASSWORD")
	t.Setenv("LEMI_SMTP_PASSWORD_FILE", f)
	if err := loadSecretEnvFiles(); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("LEMI_SMTP_PASSWORD"); got != "s3cret pass" {
		t.Fatalf("读到 %q", got)
	}
	// 两个都设置时报错，避免不知道哪个生效
	t.Setenv("LEMI_SMTP_PASSWORD", "other")
	if err := loadSecretEnvFiles(); err == nil {
		t.Fatal("同时设置变量与 _FILE 应当报错")
	}
	t.Setenv("LEMI_SMTP_PASSWORD", "")
	os.Unsetenv("LEMI_SMTP_PASSWORD")
	t.Setenv("LEMI_SMTP_PASSWORD_FILE", filepath.Join(t.TempDir(), "missing"))
	if err := loadSecretEnvFiles(); err == nil {
		t.Fatal("文件不存在应当报错")
	}
}
