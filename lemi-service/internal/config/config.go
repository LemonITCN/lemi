// Package config 读取配置：命令行参数 > 环境变量 > <数据目录>/lemi.toml > 默认值。
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// TLS 配置。
type TLS struct {
	Mode         string   `toml:"mode"` // off | manual | acme
	Cert         string   `toml:"cert"`
	Key          string   `toml:"key"`
	ACMEDomains  []string `toml:"acme_domains"`
	ACMEEmail    string   `toml:"acme_email"`
	HTTPRedirect string   `toml:"http_redirect"`
}

// Config 是全部启动配置。
type Config struct {
	DataDir        string   `toml:"-"`
	Listen         string   `toml:"listen"`
	PublicURL      string   `toml:"public_url"`
	TLS            TLS      `toml:"tls"`
	TrustedProxies []string `toml:"trusted_proxies"`
	Timezone       string   `toml:"timezone"`
	Log            struct {
		Level string `toml:"level"`
	} `toml:"log"`
	SQLite struct {
		AllowedDirs []string `toml:"allowed_dirs"`
	} `toml:"sqlite"`
	Tools struct {
		BaseURL string `toml:"base_url"`
	} `toml:"tools"`
	Update struct {
		Check *bool `toml:"check"`
	} `toml:"update"`

	// 只来自环境变量（首次启动初始化用，不写进 toml）
	MasterKey     string `toml:"-"`
	AdminUsername string `toml:"-"`
	AdminPassword string `toml:"-"`
	AdminEmail    string `toml:"-"`
	WorkspaceName string `toml:"-"`
	OpenCodeKey   string `toml:"-"`
	DefaultModel  string `toml:"-"`
	SMTP          SMTP   `toml:"-"`
	InDocker      bool   `toml:"-"`
}

// SMTP 首次启动写入的系统邮件服务器。
type SMTP struct {
	Host, Port, Username, Password, From, TLS string
}

// DefaultToolsBaseURL 是 mysqldump 工具包的默认下载地址前缀（GitHub Release，见 docs/spec.md §13）。
const DefaultToolsBaseURL = "https://github.com/LemonITCN/lemi/releases/download"

// DefaultDataDir 返回当前平台的默认数据目录。
func DefaultDataDir() string {
	if InDocker() {
		return "/data"
	}
	switch runtime.GOOS {
	case "windows":
		if isWindowsService() {
			if p := os.Getenv("ProgramData"); p != "" {
				return filepath.Join(p, "Lemi")
			}
		}
		if p := os.Getenv("LOCALAPPDATA"); p != "" {
			return filepath.Join(p, "Lemi")
		}
	case "darwin":
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, "Library", "Application Support", "Lemi")
		}
	default:
		if os.Geteuid() == 0 {
			return "/var/lib/lemi"
		}
		if x := os.Getenv("XDG_DATA_HOME"); x != "" {
			return filepath.Join(x, "lemi")
		}
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, ".local", "share", "lemi")
		}
	}
	return "lemi-data"
}

// InDocker 判断是否运行在容器里（镜像里设置 LEMI_IN_DOCKER=1）。
func InDocker() bool { return os.Getenv("LEMI_IN_DOCKER") == "1" }

// Load 按优先级合并配置。dataDir / listen 来自命令行参数（空表示未指定）。
func Load(dataDir, listen string) (*Config, error) {
	c := &Config{}
	switch {
	case dataDir != "":
		c.DataDir = dataDir
	case os.Getenv("LEMI_DATA_DIR") != "":
		c.DataDir = os.Getenv("LEMI_DATA_DIR")
	default:
		c.DataDir = DefaultDataDir()
	}
	abs, err := filepath.Abs(c.DataDir)
	if err != nil {
		return nil, err
	}
	c.DataDir = abs
	path := filepath.Join(c.DataDir, "lemi.toml")
	if b, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("配置文件 %s 格式错误：%w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	env := func(k string, dst *string) {
		if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
			*dst = strings.TrimSpace(v)
		}
	}
	list := func(k string, dst *[]string, sep string) {
		if v, ok := os.LookupEnv(k); ok && strings.TrimSpace(v) != "" {
			*dst = nil
			for _, p := range strings.Split(v, sep) {
				if p = strings.TrimSpace(p); p != "" {
					*dst = append(*dst, p)
				}
			}
		}
	}
	env("LEMI_LISTEN", &c.Listen)
	env("LEMI_PUBLIC_URL", &c.PublicURL)
	env("LEMI_TLS_MODE", &c.TLS.Mode)
	env("LEMI_TLS_CERT", &c.TLS.Cert)
	env("LEMI_TLS_KEY", &c.TLS.Key)
	list("LEMI_ACME_DOMAINS", &c.TLS.ACMEDomains, ",")
	env("LEMI_ACME_EMAIL", &c.TLS.ACMEEmail)
	env("LEMI_HTTP_REDIRECT", &c.TLS.HTTPRedirect)
	list("LEMI_TRUSTED_PROXIES", &c.TrustedProxies, ",")
	env("LEMI_TZ", &c.Timezone)
	env("LEMI_LOG_LEVEL", &c.Log.Level)
	list("LEMI_SQLITE_DIRS", &c.SQLite.AllowedDirs, string(os.PathListSeparator))
	env("LEMI_TOOLS_BASE_URL", &c.Tools.BaseURL)
	if v := os.Getenv("LEMI_UPDATE_CHECK"); v != "" {
		b, _ := strconv.ParseBool(v)
		c.Update.Check = &b
	}
	env("LEMI_MASTER_KEY", &c.MasterKey)
	env("LEMI_ADMIN_USERNAME", &c.AdminUsername)
	c.AdminPassword = os.Getenv("LEMI_ADMIN_PASSWORD") // 口令不去空白
	env("LEMI_ADMIN_EMAIL", &c.AdminEmail)
	env("LEMI_WORKSPACE_NAME", &c.WorkspaceName)
	env("LEMI_OPENCODE_API_KEY", &c.OpenCodeKey)
	env("LEMI_LLM_DEFAULT_MODEL", &c.DefaultModel)
	env("LEMI_SMTP_HOST", &c.SMTP.Host)
	env("LEMI_SMTP_PORT", &c.SMTP.Port)
	env("LEMI_SMTP_USERNAME", &c.SMTP.Username)
	c.SMTP.Password = os.Getenv("LEMI_SMTP_PASSWORD")
	env("LEMI_SMTP_FROM", &c.SMTP.From)
	env("LEMI_SMTP_TLS", &c.SMTP.TLS)
	c.InDocker = InDocker()
	if listen != "" {
		c.Listen = listen
	}
	if c.Listen == "" {
		if c.InDocker {
			c.Listen = "0.0.0.0:7700"
		} else {
			c.Listen = "127.0.0.1:7700"
		}
	}
	if c.TLS.Mode == "" {
		c.TLS.Mode = "off"
	}
	c.TLS.Mode = strings.ToLower(c.TLS.Mode)
	switch c.TLS.Mode {
	case "off", "manual", "acme":
	default:
		return nil, fmt.Errorf("tls.mode 只能是 off、manual、acme")
	}
	if c.TLS.Mode == "manual" && (c.TLS.Cert == "" || c.TLS.Key == "") {
		return nil, errors.New("TLS 手动模式需要同时指定证书与私钥文件")
	}
	if c.TLS.Mode == "acme" && len(c.TLS.ACMEDomains) == 0 {
		return nil, errors.New("自动证书模式需要指定域名（LEMI_ACME_DOMAINS）")
	}
	if c.TLS.Mode == "acme" && c.TLS.HTTPRedirect == "" {
		c.TLS.HTTPRedirect = ":80"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Tools.BaseURL == "" {
		c.Tools.BaseURL = DefaultToolsBaseURL
	}
	c.Tools.BaseURL = strings.TrimRight(c.Tools.BaseURL, "/")
	c.PublicURL = strings.TrimRight(c.PublicURL, "/")
	for i, d := range c.SQLite.AllowedDirs {
		if a, err := filepath.Abs(d); err == nil {
			c.SQLite.AllowedDirs[i] = a
		}
	}
	return c, nil
}

// UpdateCheck 是否自动检查新版本。
func (c *Config) UpdateCheck() bool { return c.Update.Check == nil || *c.Update.Check }

// Path 返回数据目录下的路径。
func (c *Config) Path(elem ...string) string {
	return filepath.Join(append([]string{c.DataDir}, elem...)...)
}
