# Lemi

Lemi 是一个可以私有部署的 Web 数据库管理工具：DataGrip 式的库表管理与 SQL 控制台、databasus 式的定时备份与健康检查，内置 AI 智能体，同一套工具还以 MCP 开放给 Claude Code、Cursor、Codex 等外部智能体。一个可执行文件就是整个系统。

> 项目处于早期开发阶段，第一个正式版发布前功能会持续补齐。架构与接口契约见 [docs/spec.md](docs/spec.md)。

<!-- 截图占位：发布第一个正式版时补上 docs/images/ 下的截图 -->
| 工作区（库表树 + SQL 控制台 + 表格） | 备份计划与记录 | AI 智能体 |
|---|---|---|
| *截图待补* | *截图待补* | *截图待补* |

## 功能

**数据库管理（对标 DataGrip）**
- MySQL（含 MariaDB）、SQLite；驱动是插件式的，以后加 PostgreSQL、MongoDB、Elasticsearch、Redis 不改主干
- SSH 隧道（主机指纹固定）、SOCKS5 / HTTP 代理，可以叠加
- 库表对象树、SQL 控制台（补全、格式化、手动事务、取消、EXPLAIN）
- 像 Excel 一样编辑的大表格：框选、复制粘贴、撤销，提交前预览 SQL，整批在一个事务里执行，乐观并发防止覆盖别人刚改的值
- 表结构设计器、导入导出（CSV、TSV、JSON、Excel、SQL、Markdown）、服务器监控、数据库账号与授权、ER 图、查询历史与收藏

**备份（对标 databasus）**
- mysqldump 或自研导出器（与 Navicat「转储 SQL 文件」格式一致），SQLite 文件快照
- zstd / gzip 压缩，可选 AES-256-GCM 加密（离线也能用 `lemi backup-file` 加备份密码解开）
- 存储目标：本地目录 / NAS、SFTP、WebDAV、FTP、SMB、S3 兼容（AWS、R2、MinIO）、腾讯云 COS、阿里云 OSS、Azure Blob、百度网盘
- 每小时 / 天 / 周 / 月 / cron 计划，GFS 保留策略，失败重试，还原与还原校验
- 连接健康检查；邮件、Webhook、钉钉、企业微信、飞书通知

**AI 智能体与 MCP**
- 服务端智能体：看表结构、跑只读查询、出图表与报告、在工作区里打开表格；写入按连接上的「AI 档位」逐条确认或自动执行，生产连接额外标记
- 定时智能体任务（例如每天早上出一份数据报告）
- MCP 端点 `/mcp`：OAuth 2.1 或个人访问令牌，每个授权单独选择每个连接的档位

**安全（设计前提是直接暴露在公网）**
- 首次启动一次性初始化令牌，管理员与空间管理员强制两步验证，登录限速与锁定
- 连接口令等密钥字段级加密、只写不读；审计每一条执行的 SQL
- 只读会话在数据库层强制；MySQL 禁止 `LOAD DATA LOCAL INFILE`；SQLite 只能打开白名单目录
- 多用户与工作空间、中文与英文界面，电脑、平板、手机浏览器都能用

## 快速开始

三种方式任选其一。

### 方式一：下载可执行文件直接运行

到 [Releases](https://github.com/LemonITCN/lemi/releases/latest) 下载对应系统的包：

| 系统 | 文件 |
|---|---|
| Windows 10 / 11，x64 | `lemi-<版本>-windows-amd64.zip` |
| Windows 10 / 11，ARM64（原生 ARM64 程序） | `lemi-<版本>-windows-arm64.zip` |
| macOS，Apple 芯片 | `lemi-<版本>-darwin-arm64.tar.gz` |
| macOS，Intel | `lemi-<版本>-darwin-amd64.tar.gz` |
| Linux，x64 | `lemi-<版本>-linux-amd64.tar.gz` |
| Linux，ARM64 | `lemi-<版本>-linux-arm64.tar.gz` |

```sh
tar -xzf lemi-*-linux-amd64.tar.gz
./lemi                      # 等同于 lemi serve，默认只监听 127.0.0.1:7700
```

浏览器打开 <http://127.0.0.1:7700>，填控制台里打印的初始化令牌（见下文「首次启动」）。

- 让其他机器访问：`./lemi serve --listen 0.0.0.0:7700`，并按「公网部署安全清单」启用 HTTPS。
- Windows：解压后在 PowerShell 里运行 `.\lemi.exe`。
- macOS：程序用 Lemi 专用证书签名但没有经过苹果公证，用浏览器下载的首次打开会被拦下——到「系统设置 → 隐私与安全性」底部点「仍要打开」，或在终端执行 `xattr -d com.apple.quarantine lemi` 去掉隔离标记。

### 方式二：装成系统服务（开机自启）

Linux（systemd）与 macOS（launchd）：

```sh
sudo install -m 755 lemi /usr/local/bin/lemi
sudo lemi service install --listen 0.0.0.0:7700
sudo lemi service start
sudo cat /var/lib/lemi/setup-token          # macOS 以 root 运行时在 /var/root/Library/Application Support/Lemi/
```

Windows（以管理员身份打开 PowerShell，先把 `lemi.exe` 放到固定位置，例如 `C:\Program Files\Lemi\`）：

```powershell
.\lemi.exe service install --listen 0.0.0.0:7700
.\lemi.exe service start
Get-Content "$env:ProgramData\Lemi\setup-token"
```

`lemi service status|stop|restart|uninstall` 管理服务；`--data <目录>` 可以指定数据目录。

### 方式三：Docker Compose

```sh
mkdir lemi && cd lemi
curl -fsSLO https://raw.githubusercontent.com/LemonITCN/lemi/main/docker/docker-compose.yml
docker compose up -d
docker compose logs lemi | grep -A3 初始化令牌
```

镜像 `ghcr.io/lemonitcn/lemi`（linux/amd64、linux/arm64，非 root 运行，预装 mysqldump），数据都在卷 `lemi-data`（容器内 `/data`）。[docker-compose.yml](docker/docker-compose.yml) 里每个配置项都有中文注释，口令用 Docker secrets 传入。要自动 HTTPS，用 [docker-compose.caddy.yml](docker/docker-compose.caddy.yml) + [Caddyfile](docker/Caddyfile)（Caddy 自动申请证书）。

升级：`docker compose pull && docker compose up -d`。

## 首次启动

- **初始化令牌**：数据库里还没有任何用户时，Lemi 在控制台（Docker 为容器日志）打印一个一次性初始化令牌，同时写进数据目录的 `setup-token` 文件（0600）。初始化页面必须填这个令牌才能创建第一个管理员——防止服务刚暴露在公网就被人抢先注册。初始化完成后令牌自动删除。
- **跳过初始化页**：设置 `LEMI_ADMIN_USERNAME` 与 `LEMI_ADMIN_PASSWORD`（或 `LEMI_ADMIN_PASSWORD_FILE`）后，首次启动直接创建实例管理员。
- **强制两步验证**：实例管理员、空间所有者与管理员必须绑定 TOTP 两步验证（同时给出 10 个一次性恢复码，只显示一次）；口令正确但还没绑定时只能进入绑定页面。普通成员是否强制由实例管理员决定（默认强制）。
- 口令至少 10 位，拒绝常见弱口令。忘记口令时在服务器上运行 `lemi admin reset-password <用户名>`（需要能读写数据目录）。

## 公网部署安全清单

1. **启用 HTTPS**，三种方式任选：
   - 反向代理：Caddy / Nginx 等负责证书，Lemi 只监听本机或内网（`LEMI_TLS_MODE=off`）。Docker 用户直接用 `docker-compose.caddy.yml`。
   - Lemi 自动申请证书：`LEMI_TLS_MODE=acme`、`LEMI_ACME_DOMAINS=lemi.example.com`、`LEMI_LISTEN=0.0.0.0:443`，80 端口要能从公网访问（证书验证与 HTTP 跳转）。
   - 自己的证书：`LEMI_TLS_MODE=manual`、`LEMI_TLS_CERT`、`LEMI_TLS_KEY`。

   以 HTTP 从公网访问时，界面顶部会一直显示红色警告。
2. **受信代理**：放在反向代理后面时，把代理的 IP 或网段写进 `LEMI_TRUSTED_PROXIES`，Lemi 只采信它们传来的 `X-Forwarded-For` / `X-Forwarded-Proto`。没有反向代理时**不要**设置，否则别人可以伪造来源 IP 绕过登录限速。
3. **对外地址**：设置 `LEMI_PUBLIC_URL=https://lemi.example.com`，通知、分享链接、MCP 授权里的地址才是对的。
4. **网络策略**：实例管理员在系统设置的网络策略里限制数据库连接允许 / 拒绝的网段；云元数据地址（169.254.169.254 等）永远拒绝；Webhook、模型自定义地址默认不能访问内网。
5. **备份加密**：备份计划开启加密并设置备份密码（至少 12 位）。备份文件放在云存储上也安全；Lemi 的数据目录丢了，用 `lemi backup-file decrypt` 加备份密码仍能离线解开。
6. **主密钥**：默认自动生成在数据目录 `master.key`；拿到数据目录备份就同时拿到了密钥。更安全的做法是用 `LEMI_MASTER_KEY`（或 `LEMI_MASTER_KEY_FILE`）从外部传入，并另外妥善备份——丢了它，加密保存的连接口令与密钥都无法解开。
7. **只开放必要端口**，数据库连接尽量用只读账号或 Lemi 的只读开关，生产连接打上「生产」标签（界面红框，智能体写入额外标记）。
8. **备份数据目录**（复制整个目录即可迁移或恢复），并保持 Lemi 为最新版本（管理员界面会提示新版本）。

## 配置

优先级：命令行参数 > 环境变量 > 数据目录里的 `lemi.toml` > 默认值。每一项都有环境变量，Docker 只写环境变量就能完整配置。

| 环境变量 | lemi.toml 键 | 默认 | 说明 |
|---|---|---|---|
| `LEMI_DATA_DIR` | — | 见下 | 数据目录（全部数据都在这里） |
| `LEMI_LISTEN` | `listen` | `127.0.0.1:7700`（Docker `0.0.0.0:7700`） | 监听地址 |
| `LEMI_PUBLIC_URL` | `public_url` | 空 | 对外地址（通知、分享链接、OAuth 用）；空时按请求推断 |
| `LEMI_TLS_MODE` | `tls.mode` | `off` | `off` / `manual` / `acme` |
| `LEMI_TLS_CERT` / `LEMI_TLS_KEY` | `tls.cert` / `tls.key` | | manual 模式的证书与私钥文件 |
| `LEMI_ACME_DOMAINS` | `tls.acme_domains` | | acme 模式的域名，逗号分隔 |
| `LEMI_ACME_EMAIL` | `tls.acme_email` | | acme 账号邮箱 |
| `LEMI_HTTP_REDIRECT` | `tls.http_redirect` | `:80`（acme 时） | HTTP 跳转与 HTTP-01 验证端口 |
| `LEMI_TRUSTED_PROXIES` | `trusted_proxies` | 空 | 反向代理的 IP / 网段，逗号分隔 |
| `LEMI_MASTER_KEY` | — | | 主密钥（32 字节的 base64）。设置后不读也不生成 `master.key` |
| `LEMI_ADMIN_USERNAME` / `LEMI_ADMIN_PASSWORD` | — | | 首次启动直接创建实例管理员（跳过初始化令牌），首次登录仍强制绑定两步验证 |
| `LEMI_ADMIN_EMAIL` | — | | 初始管理员邮箱 |
| `LEMI_WORKSPACE_NAME` | — | 默认空间 | 首次启动创建的工作空间名 |
| `LEMI_OPENCODE_API_KEY` | — | | 首次启动写入 OpenCode Go 模型密钥 |
| `LEMI_LLM_DEFAULT_MODEL` | — | | 默认模型 |
| `LEMI_SQLITE_DIRS` | `sqlite.allowed_dirs` | 空 | 允许打开的服务器 SQLite 目录，按系统路径分隔符（Linux / macOS `:`，Windows `;`）分隔 |
| `LEMI_SMTP_HOST` / `PORT` / `USERNAME` / `PASSWORD` / `FROM` / `TLS` | — | | 首次启动写入系统邮件服务器（找回口令、通知） |
| `LEMI_TOOLS_BASE_URL` | `tools.base_url` | GitHub Release | mysqldump 工具包下载地址前缀 |
| `LEMI_LOG_LEVEL` | `log.level` | `info` | `debug` / `info` / `warn` / `error` |
| `LEMI_UPDATE_CHECK` | `update.check` | `true` | 每天检查一次新版本 |
| `LEMI_TZ` | `timezone` | 系统时区 | 计划任务的时区，例如 `Asia/Shanghai` |

- 「首次启动写入」类变量只在对应设置为空时写入一次，之后以界面上的设置为准，重启不会覆盖。
- 口令类变量（`LEMI_MASTER_KEY`、`LEMI_ADMIN_PASSWORD`、`LEMI_OPENCODE_API_KEY`、`LEMI_SMTP_PASSWORD` 等）都可以改用 `<变量>_FILE` 指向一个文件（Docker secrets）。
- 默认数据目录：Linux root `/var/lib/lemi`，Linux 普通用户 `~/.local/share/lemi`，macOS `~/Library/Application Support/Lemi`，Windows 服务 `%ProgramData%\Lemi`，Windows 用户 `%LOCALAPPDATA%\Lemi`，Docker `/data`。

### 命令行

```
lemi serve [--data 目录] [--listen 地址]          前台运行（默认命令）
lemi service install|uninstall|start|stop|restart|status [--data 目录] [--listen 地址]
                                                  装成 systemd / launchd / Windows 服务
lemi admin reset-password <用户名>                 重置口令并清除两步验证，打印临时口令
lemi admin reset-totp <用户名>
lemi admin unlock <用户名|IP>                      解除登录锁定
lemi update [--check]                             检查并升级到最新正式版（校验发布签名）
lemi backup-file decrypt|to-sql <备份文件> [-o 输出]  离线解密 / 解压备份为 .sql
lemi healthcheck                                  检查本机服务是否正常（Docker 用）
lemi version
```

## 在线升级

- Lemi 每天检查一次 GitHub 上的最新正式版（`LEMI_UPDATE_CHECK=false` 关闭），有新版本时管理员界面会提示，实例管理员可以一键升级；命令行用 `lemi update`。
- 升级包必须通过发布签名校验：每个 Release 附带 `SHA256SUMS` 与 `SHA256SUMS.sig`（Ed25519），程序里内置公钥，签名不对、哈希不对、版本更旧都会拒绝。
- 装成系统服务或前台运行的都会自动重启到新版本；Docker 部署不在容器里自升级，用 `docker compose pull && docker compose up -d`。

手工校验下载的文件：

```sh
sha256sum -c SHA256SUMS --ignore-missing
cat > lemi-release.pub.pem <<'EOF'
-----BEGIN PUBLIC KEY-----
MCowBQYDK2VwAyEAKy2GRU1PiUfbfJrwGpaDtzY3W976kZbs3GnRLjWIhQ8=
-----END PUBLIC KEY-----
EOF
base64 -d SHA256SUMS.sig > SHA256SUMS.sig.bin
openssl pkeyutl -verify -pubin -inkey lemi-release.pub.pem -rawin -in SHA256SUMS -sigfile SHA256SUMS.sig.bin
```

## MCP 接入

在 Lemi 的设置页里可以创建个人访问令牌（`lemi_pat_...`，只显示一次）并一键复制下面的配置。不带令牌时客户端会走 OAuth 授权：浏览器里登录 Lemi，选择工作空间与每个连接的档位。

**Claude Code**

```sh
claude mcp add --transport http lemi https://你的地址/mcp
# 或者用个人访问令牌
claude mcp add --transport http lemi https://你的地址/mcp --header "Authorization: Bearer lemi_pat_xxx"
```

**Cursor**（`~/.cursor/mcp.json`）

```json
{
  "mcpServers": {
    "lemi": {
      "url": "https://你的地址/mcp",
      "headers": { "Authorization": "Bearer lemi_pat_xxx" }
    }
  }
}
```

**Codex**（`~/.codex/config.toml`，令牌放在环境变量 `LEMI_TOKEN` 里）

```toml
[mcp_servers.lemi]
url = "https://你的地址/mcp"
bearer_token_env_var = "LEMI_TOKEN"
```

**Claude Desktop**：设置 → Connectors → Add custom connector，填 `https://你的地址/mcp`。

## 从源码构建

需要 Go 1.26 及以上、Node.js 22 及以上（npm）。

```sh
scripts/build.sh                    # 构建前端 + 全部六个平台，产物在 dist/
scripts/build.sh linux/amd64        # 只构建一个平台；也可以写 linux、darwin、windows
scripts/build.sh --skip-console     # 前端已经构建过时跳过
```

`dist/` 里是 `lemi-<版本>-<系统>-<架构>.tar.gz|zip` 与 `SHA256SUMS`，`dist/bin/<系统>-<架构>/` 里是可执行文件。纯 Go、关闭 CGO，前端产物嵌进可执行文件。

开发时：

```sh
cd lemi-console && npm ci && npm run build && cp -R dist/. ../lemi-service/internal/web/dist/
cd ../lemi-service && go run ./cmd/lemi serve --data ./tmp-data
```

自己构建 Docker 镜像（构建上下文是仓库根目录；国内网络可以加 Dockerfile 开头注释里的镜像源参数）：

```sh
docker build -f docker/Dockerfile -t lemi .
```

## 平台支持

| 系统 | 架构 | 说明 |
|---|---|---|
| Windows 10 / 11 | x64 | |
| Windows 10 / 11 | ARM64 | 原生 ARM64 程序。MySQL 官方没有 ARM64 版 mysqldump：Windows 11 ARM 用 x64 仿真运行 x64 版；**Windows 10 ARM 不能运行 x64 程序，MySQL 备份只能用 Lemi 自研导出器** |
| macOS 12 及以上 | Apple 芯片、Intel | Lemi 专用证书签名（未公证），首次打开需在「隐私与安全性」里放行 |
| Linux | x64、ARM64 | 静态单文件，不依赖 glibc 版本；mysqldump 由 Lemi 按需下载 |
| Docker | linux/amd64、linux/arm64 | 镜像内预装 mysqldump |

浏览器：最近两年的 Chrome、Edge、Firefox、Safari，电脑、平板、手机都适配。

## 安全问题

发现安全漏洞请不要公开提 Issue，用 GitHub 的 [私下报告漏洞](https://github.com/LemonITCN/lemi/security/advisories/new)。

## 许可证

[MIT](LICENSE)

---

## English

Lemi is a self-hosted web database tool: DataGrip-style schema browsing, SQL console and spreadsheet-like data editing; databasus-style scheduled backups (mysqldump or a built-in Navicat-compatible dumper, compression, encryption, local/SFTP/WebDAV/FTP/SMB/S3/COS/OSS/Azure targets, retention, restore verification) with health checks and notifications; plus a built-in AI agent whose tools are also exposed over MCP (OAuth 2.1 or personal access tokens) to Claude Code, Cursor, Codex and others. It ships as a single executable with the web UI embedded, supports MySQL/MariaDB and SQLite (pluggable drivers), multiple users and workspaces, and is designed to be exposed on the public internet (setup token, mandatory TOTP for admins, rate limiting, field-level encryption, full SQL audit).

Quick start: download a package from [Releases](https://github.com/LemonITCN/lemi/releases/latest) and run `./lemi` (listens on `127.0.0.1:7700`; the one-time setup token is printed to the console), install it as a service with `lemi service install`, or use Docker: `docker compose up -d` with [docker/docker-compose.yml](docker/docker-compose.yml). Builds are available for Windows 10/11 (x64 and native ARM64), macOS (Apple silicon and Intel), Linux (x64 and ARM64) and Docker (linux/amd64, linux/arm64). Releases are signed (Ed25519 over `SHA256SUMS`) and the built-in updater only installs correctly signed packages. Build from source with `scripts/build.sh` (Go 1.26+, Node.js 22+). Licensed under MIT.
