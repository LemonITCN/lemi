package main

import (
	"fmt"
	"os"
	"strings"
)

// secretEnvFromFiles 让口令类环境变量可以从文件读取（Docker secrets 的惯例）：
// 设置了 LEMI_ADMIN_PASSWORD_FILE=/run/secrets/admin_password 而没设置 LEMI_ADMIN_PASSWORD 时，
// 读取文件内容（去掉末尾换行）作为 LEMI_ADMIN_PASSWORD。这样口令不出现在 compose 文件与 docker inspect 里。
var secretEnvFromFiles = []string{
	"LEMI_MASTER_KEY",
	"LEMI_ADMIN_USERNAME",
	"LEMI_ADMIN_PASSWORD",
	"LEMI_ADMIN_EMAIL",
	"LEMI_OPENCODE_API_KEY",
	"LEMI_SMTP_USERNAME",
	"LEMI_SMTP_PASSWORD",
}

// 文件名排在 main.go 之后，但在 main() 之前执行，早于任何 config.Load。
func init() {
	if err := loadSecretEnvFiles(); err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func loadSecretEnvFiles() error {
	for _, k := range secretEnvFromFiles {
		path := os.Getenv(k + "_FILE")
		if path == "" {
			continue
		}
		if v, ok := os.LookupEnv(k); ok && v != "" {
			return fmt.Errorf("%s 与 %s_FILE 只能设置一个", k, k)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("读取 %s_FILE 指定的文件失败：%w", k, err)
		}
		if err := os.Setenv(k, strings.TrimRight(string(b), "\r\n")); err != nil {
			return err
		}
	}
	return nil
}
