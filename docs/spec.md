# Lemi 架构契约

本文是 lemi-service（Go）与 lemi-console（Vue 3）共同遵守的契约。改数据格式、接口、工具名、权限规则之前先改这里。

## 1. 产品定位

- 纯 Web 的数据库管理工具：DataGrip 式的库表树、SQL 控制台、像 Excel 一样编辑的大表格；databasus 式的定时备份、健康检查与通知；内置 AI 智能体（服务端 agent loop），同一套工具同时以 MCP 暴露给 Claude Code、Cursor、Codex 等外部智能体。
- 一个可执行文件：Go 服务内嵌 Vue 3 前端产物，运行一个程序就是整个系统。
- 第一期数据库：MySQL（含 MariaDB）、SQLite。驱动做成插件式（§6），以后加 PostgreSQL、MongoDB、Elasticsearch、Redis 不改主干。
- 多用户 + 工作空间；界面中文 + 英文；电脑、平板、手机浏览器完整适配。
- 设计前提：服务会直接暴露在公网。安全是第一优先级（§5）。

## 2. 仓库结构

```
lemi/
  docs/spec.md                 本文
  lemi-service/                Go 服务（module github.com/LemonITCN/lemi/lemi-service）
    cmd/lemi/                  命令行入口：serve / admin / service / update / backup-file / version
    internal/...               见 §22
  lemi-console/                Vue 3 + Vite + TypeScript 前端，dist 打包时嵌进服务
  docker/                      Dockerfile、docker-compose.yml
  scripts/                     本地构建脚本（CI 也用）
  .github/workflows/
    build.yml                  推送 main / PR：编译与测试；推送 v* 标签：发布 Release 与镜像
    mysql-tools.yml            手动触发：从 MySQL 官方提取 mysqldump/mysql 发布到 tools-mysql-* Release（§13）
  .secrets/                    本机测试密钥（gitignore，绝不提交）
```

公开仓库：任何密钥、证书私钥、测试账号密码都不进 git。CI 用 GitHub Secrets；本机用 `.secrets/` 或 `~/.lemi-secrets/`。CI 跑 gitleaks 扫描，发现即失败。

## 3. 运行与数据目录

### 3.1 数据目录 = 全部数据

数据目录只放文件，元数据用 SQLite。复制整个目录到另一台机器，`lemi serve --data <目录>` 就能原样启动。

```
<数据目录>/
  lemi.toml            配置文件（可选；环境变量与命令行参数优先）
  master.key           32 字节主密钥（0600）。加密连接密码、云存储密钥、模型密钥、两步验证密钥
  lemi.db              元数据库（SQLite，WAL 模式）
  lemi.lock            进程锁：同一目录只允许一个进程
  snapshots/           元数据库的一致性快照（VACUUM INTO，每小时一份，保留 3 份）。复制运行中的目录时，
                       lemi.db 可能处于写入中途，快照一定是完整的；启动时 lemi.db 损坏会提示用最新快照恢复
  sqlite/              上传的 SQLite 数据库文件（§7.4）
  backups/             「本地目录」备份目标的默认根
  tools/               按需下载的 mysqldump / mysql（§13）
  results/             智能体与 MCP 的大结果集（7 天后清理）
  acme/                自动申请的证书
  logs/                运行日志（按天轮转，保留 14 天）
```

默认位置：Linux root `/var/lib/lemi`，Linux 普通用户 `~/.local/share/lemi`，macOS `~/Library/Application Support/Lemi`，Windows 服务 `%ProgramData%\Lemi`，Windows 用户 `%LOCALAPPDATA%\Lemi`。Docker 固定 `/data`。

### 3.2 配置项

优先级：命令行参数 > 环境变量 > `lemi.toml` > 默认值。每一项都有对应的环境变量，Docker Compose 只写环境变量就能完整配置。

| 环境变量 | toml 键 | 默认 | 说明 |
|---|---|---|---|
| LEMI_DATA_DIR | — | 见上 | 数据目录 |
| LEMI_LISTEN | listen | 127.0.0.1:7700（Docker 0.0.0.0:7700） | 监听地址 |
| LEMI_PUBLIC_URL | public_url | 空 | 对外地址（OAuth、分享链接、通知里的链接用）。空时按请求推断 |
| LEMI_TLS_MODE | tls.mode | off | off / manual / acme |
| LEMI_TLS_CERT / LEMI_TLS_KEY | tls.cert / tls.key | | manual 模式证书文件 |
| LEMI_ACME_DOMAINS | tls.acme_domains | | acme 模式域名，逗号分隔 |
| LEMI_ACME_EMAIL | tls.acme_email | | |
| LEMI_HTTP_REDIRECT | tls.http_redirect | :80（acme 时） | HTTP 跳转与 HTTP-01 验证端口 |
| LEMI_TRUSTED_PROXIES | trusted_proxies | 空 | 反向代理的 IP/网段，只信任它们传来的 X-Forwarded-For / X-Forwarded-Proto |
| LEMI_MASTER_KEY | — | | 主密钥（base64，32 字节）。设置后不读 master.key；首次启动也不生成文件 |
| LEMI_ADMIN_USERNAME / LEMI_ADMIN_PASSWORD | — | | 首次启动直接创建实例管理员（跳过初始化令牌）。首次登录仍强制绑定两步验证 |
| LEMI_ADMIN_EMAIL | — | | |
| LEMI_WORKSPACE_NAME | — | 默认空间 | 首次启动创建的工作空间名 |
| LEMI_OPENCODE_API_KEY | — | | 首次启动写入 OpenCode Go 供应商密钥 |
| LEMI_LLM_DEFAULT_MODEL | — | | 默认模型 |
| LEMI_SQLITE_DIRS | sqlite.allowed_dirs | 空 | 允许打开的服务器 SQLite 目录，系统路径分隔符分隔（§7.4） |
| LEMI_SMTP_HOST/PORT/USERNAME/PASSWORD/FROM/TLS | — | | 首次启动写入系统邮件服务器（找回密码、通知） |
| LEMI_TOOLS_BASE_URL | tools.base_url | GitHub Release 地址 | mysqldump 下载地址前缀（以后换 CDN 只改这里） |
| LEMI_LOG_LEVEL | log.level | info | debug / info / warn / error |
| LEMI_UPDATE_CHECK | update.check | true | 启动后每天检查新版本 |
| LEMI_TZ | timezone | 系统时区 | 计划任务的时区 |

「首次启动写入」类变量只在对应项为空时写入数据库，之后以界面上的设置为准，避免重启覆盖用户的修改。

口令类变量（LEMI_MASTER_KEY、LEMI_ADMIN_USERNAME、LEMI_ADMIN_PASSWORD、LEMI_ADMIN_EMAIL、LEMI_OPENCODE_API_KEY、LEMI_SMTP_USERNAME、LEMI_SMTP_PASSWORD）都可以改用 `<变量>_FILE` 指向一个文件（Docker secrets 的惯例，去掉末尾换行），两者同时设置时报错。由命令层（cmd/lemi/envfile.go）在读取配置之前处理。

### 3.3 命令行

```
lemi serve [--data DIR] [--listen ADDR]        启动（默认命令）
lemi admin reset-password <用户名>              重置密码并清除两步验证，打印临时密码（需要本机文件权限）
lemi admin reset-totp <用户名>
lemi admin unlock <用户名|IP>                   解除登录锁定
lemi service install|uninstall|start|stop|status [--data DIR] [--listen ADDR]
                                               装成 systemd / launchd / Windows 服务
lemi update [--check]                          检查并升级到最新正式版（校验发布签名）
lemi healthcheck [--timeout 5s]                请求本机 /healthz（Docker HEALTHCHECK 用；按 LEMI_LISTEN 选地址，TLS 时只连本机并跳过证书校验）
lemi backup-file decrypt|to-sql <备份文件> [-o 输出]   离线解密 / 解压备份为 .sql（需要备份密码）
lemi version
```

## 4. 账号、工作空间与权限

### 4.1 实例级

- 角色：实例管理员（admin）、普通用户（user）。实例管理员管理用户、系统设置、模型供应商、全局安全策略，能看所有工作空间（进入时记审计）。
- 首次启动：没有任何用户时，控制台和日志打印一次性初始化令牌（32 字节随机，同时写进 `<数据目录>/setup-token`，0600）。初始化页面必须填这个令牌才能创建第一个管理员。防止服务暴露在公网时被人抢先注册。设置了 LEMI_ADMIN_USERNAME/PASSWORD 时直接创建，不出初始化页。
- 用户由管理员创建（或邀请链接注册，链接一次性、24 小时有效）。没有开放注册。

### 4.2 工作空间

- 数据库连接、备份计划、存储目标、通知渠道、智能体对话、报告、定时任务都属于某个工作空间。
- 空间角色（逐级包含）：
  - viewer：查看连接与结构；查询走只读会话（数据库层强制，§8.3）；看备份记录与健康状态。
  - member：读写数据（增删改）、DDL、导入导出、发起备份与还原、使用智能体。
  - admin：管理连接、备份、存储、通知、定时任务、空间的 AI 设置与成员。
  - owner：删除空间、转让所有权。
- 单个连接可以对单个成员再收紧（none / read / write），只能收紧不能放宽。
- 用户的有效权限 = min(空间角色, 连接上的收紧)。智能体与 MCP 以用户身份行事，再受 AI 档位限制（§16.4），取三者最严。

## 5. 安全

### 5.1 登录与会话

- 密码 argon2id（m=64MB, t=3, p=2），最少 10 位，拒绝常见弱密码表前 10 万条中的密码。
- 两步验证 TOTP（6 位、30 秒、±1 窗口、同一码不能用两次）+ 10 个一次性恢复码（只显示一次，存哈希）。实例管理员和空间 owner/admin 必须绑定：密码正确但没绑定时会话处于「待绑定」状态，只能访问绑定接口。普通成员是否强制由实例管理员设置（默认强制）。
- 登录限速：每 IP 每分钟 10 次、每账号连续失败 5 次锁 15 分钟（之后翻倍，最长 24 小时）。失败只回「用户名或密码错误」，不区分。锁定与解锁记审计并通知该用户。
- 新设备 / 新 IP 登录、两步验证被关闭、密码修改时通知用户本人（邮件或其个人通知渠道）。
- 会话：服务端随机令牌（32 字节）存哈希。Cookie `lemi_session`：HttpOnly、SameSite=Lax、HTTPS 下 Secure、Path=/。空闲 12 小时失效，最长 7 天。改密码、重置两步验证时吊销该用户其他会话。可在设置里看并踢掉登录设备。
- 敏感操作（改密码、关两步验证、查看/导出连接密码以外的密钥都不允许、删除工作空间、创建 MCP 令牌）需要 10 分钟内输过密码或两步验证码（re-auth）。

### 5.2 Web 防护

- CSRF：所有非 GET 的 /api 请求必须带 `X-Lemi-CSRF` 头，值等于会话绑定的 CSRF 令牌；同时校验 Origin / Referer 与本站一致。
- 响应头：`Content-Security-Policy: default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; connect-src 'self'; font-src 'self' data:; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'`；`X-Content-Type-Options: nosniff`；`Referrer-Policy: no-referrer`；`X-Frame-Options: DENY`；`Permissions-Policy` 关闭摄像头/麦克风/定位；HTTPS 下 `Strict-Transport-Security: max-age=31536000`。
- 前端不使用 v-html 渲染不可信内容；Markdown 渲染关闭 HTML，再经 DOMPurify；ECharts option 经服务端白名单清洗（§16.6）；Mermaid securityLevel=strict。
- WebSocket 校验 Origin 与会话。
- 请求体默认上限 2 MB（导入与上传接口单独放宽并流式处理）。
- 公网地址上以 HTTP 访问时，界面顶部持续显示红色警告。

### 5.3 密钥

- 字段级 AES-256-GCM，密钥由主密钥经 HKDF 派生，附加数据（AAD）= 表名 + 记录 ID + 字段名，防止密文被挪到别的记录。
- 密钥字段只写不读：接口永远不返回明文，返回 `{"set": true}`；更新时不传表示保持原值。连接的主机或端口改了必须重新输入密码（防止把旧密码带去别的地址）。
- 日志、审计、错误信息里不出现密码、连接串、令牌；DSN 由结构化字段拼出，额外参数只接受白名单键。

### 5.4 数据库连接相关

- MySQL 驱动永不开启 `allowAllFiles`、不注册本地文件：`LOAD DATA LOCAL INFILE` 一律失败，防止恶意数据库服务器读取 Lemi 主机文件。
- SQLite 只能打开白名单目录与上传目录下的文件（§7.4）；`ATTACH`、`VACUUM INTO`、`load_extension` 在 SQLite 连接上一律禁止。
- 网络策略（实例管理员设置）：连接目标的允许/拒绝网段。云元数据地址（169.254.169.254、fd00:ec2::254 等）永远拒绝。Webhook、通知、模型自定义地址默认拒绝内网与回环（可由管理员放开），DNS 解析后按实际 IP 校验并固定，防 DNS 重绑定。
- SSH 隧道首次连接显示主机指纹，确认后固定；指纹变化拒绝连接并提示。

### 5.5 资源上限

- 查询超时默认 5 分钟（界面可改，智能体默认 60 秒）；单个结果集在服务端最多缓存 10 万行或 64 MB；单个单元格传给前端时超过 64 KB 截断（可按需取全文，上限 16 MB）。
- 每用户同时运行的查询 8 个，每连接的服务端连接池上限 10。

### 5.6 审计

所有登录、权限变更、连接与备份配置变更、每一条执行的 SQL（来源：界面 / 表格编辑 / 智能体 / MCP / 定时任务，含连接、库、耗时、影响行数、错误）、导出、下载、还原都记审计。保留期默认 365 天，可在界面按人、空间、连接、来源检索并导出 CSV。

## 6. 驱动插件架构

### 6.1 服务端

```go
package driver

// 每个驱动在 init() 里 driver.Register(d)。
type Driver interface {
    Info() Info                        // ID、名称、类别、图标、连接表单、查询语言、能力清单
    Open(ctx context.Context, cfg *ConnConfig, dial Dialer) (Conn, error)
}

type Info struct {
    ID            string       // "mysql" | "sqlite" | 以后 "postgres" | "mongodb" | "elasticsearch" ...
    Name          string
    Category      string       // relational | document | search | keyvalue
    QueryLanguage string       // "sql:mysql" | "sql:sqlite" | 以后 "mongo" | "es-dsl"
    DefaultPort   int
    Form          []FormField  // 连接表单（前端按此渲染，加驱动不用改前端表单）
    Capabilities  []string     // 见下
}
```

能力是可选接口，前端与智能体按能力清单决定显示什么、给什么工具：

| 能力 | 接口 | 作用 |
|---|---|---|
| navigate | `Navigator` | 对象树：`Children(ctx, path)` 返回通用节点（类型、名称、图标、可否展开、可用操作） |
| introspect | `Introspector` | 库、表、列、索引、外键、触发器、视图、例程元数据；补全用的结构快照 |
| query | `Querier` | 打开会话 `Session`：执行、取消、事务、游标分页 |
| edit | `Editor` | 表格变更集（§9）生成并执行 SQL |
| ddl | `DDLer` | 取对象 DDL；表结构设计器的差异 → ALTER 语句 |
| dump | `Dumper` | 自研 SQL 导出与导入（§11） |
| backup | `BackupEngine` | 备份方式清单与执行（mysqldump、自研导出、SQLite 文件快照） |
| monitor | `Monitor` | 进程列表、杀会话、状态变量、慢查询、表空间排行 |
| users | `UserAdmin` | 数据库账号、授权、一键建只读账号 |
| readonly | 会话选项 | 会话层强制只读（§8.3） |

文档型、搜索型数据库以后实现 `navigate` + `introspect` + 自己的 `Querier`（查询语言不同），表格按顶层字段展开，复杂值在值面板看 JSON。

### 6.2 前端

`src/drivers/<id>/index.ts` 注册：查询语言的编辑器扩展（CodeMirror 语言、补全、语句切分）、对象图标、特有面板（如 MySQL 的进程列表）。其余页面（连接表单、对象树、表格、导出）全部按服务端的 `Info` 与能力清单通用渲染。

## 7. 连接

### 7.1 字段

- 通用：名称、颜色、分组文件夹、环境标签（生产 / 测试 / 开发，生产连接整个界面带红色边框）、只读开关（连接级强制只读，所有人包括管理员）、备注。
- MySQL：主机、端口、用户名、密码、默认库、字符集（默认 utf8mb4）、会话时区（可选，空表示用服务器默认；TIMESTAMP 列按它显示与写入）、TLS（disable / preferred / required / verify-ca / verify-full，可上传 CA、客户端证书与私钥）、连接超时、白名单额外参数。
- SQLite：文件（白名单目录内的相对路径，或上传文件的 ID）、只读打开。

### 7.2 网络通道（可叠加：代理 → SSH → 数据库）

- SSH 隧道：主机、端口、用户、密码或私钥（可带口令）、主机指纹固定（§5.4）、keepalive。
- 代理：SOCKS5（可带用户名密码）、HTTP CONNECT（可带 Basic 认证）。代理也可以用于连 SSH 跳板。
- 所有拨号都经网络策略（§5.4）检查。

### 7.3 连接池

每个连接按「用户 × 会话类型」懒建池；空闲 10 分钟关闭；SSH 隧道与池同生命周期，多个池复用同一条 SSH 连接。

### 7.4 SQLite 文件

- 实例管理员配置允许的目录（LEMI_SQLITE_DIRS）。用户填相对路径，服务端 `filepath.Clean` 后拼接，再 `EvalSymlinks` 确认真实路径仍在白名单目录内，拒绝 `..`、绝对路径、设备文件。
- 上传：.db / .sqlite / .sqlite3，校验文件头 `SQLite format 3\0`，存 `<数据目录>/sqlite/<id>.db`，可下载回来（下载前做一次 `VACUUM INTO` 得到一致副本）。

## 8. 查询执行

### 8.1 会话

- 控制台标签页对应一个服务端会话（固定一条数据库连接），支持手动事务（自动提交 / 手动，提交、回滚按钮，有未提交事务时标签显示圆点）。空闲 30 分钟、或浏览器断开 5 分钟后回滚并释放。
- 表格页用短会话：读用池，提交变更时开事务执行全部语句。

### 8.2 执行

- `POST /api/v1/sessions/{sid}/execute {sql, params?, max_rows?}`：按语句切分（§8.4）逐条执行，返回每条的结果（结果集 / 影响行数 / 错误、耗时）。结果集首批 500 行，游标保留在会话里，`fetch` 继续取，直到下次执行或会话关闭。
- 取消：MySQL 用另一条连接 `KILL QUERY <thread_id>`；SQLite 取消 context（sqlite3_interrupt）。
- EXPLAIN：MySQL `EXPLAIN FORMAT=JSON`（5.7+）以树形展示，SQLite `EXPLAIN QUERY PLAN`。

### 8.3 只读会话（数据库层强制，不靠猜 SQL）

- MySQL：`SET SESSION TRANSACTION READ ONLY` 后每条语句在 `START TRANSACTION READ ONLY` 中执行；同时语句分类器只放行 SELECT / WITH / TABLE / VALUES / SHOW / DESCRIBE / EXPLAIN / USE / HELP，拒绝 `SELECT ... INTO OUTFILE|DUMPFILE`、`SET GLOBAL`、`KILL`、`LOCK`、`FLUSH`、`LOAD`、`HANDLER` 等；禁止多语句。`max_execution_time` 限制 SELECT 时长。
- SQLite：只读打开（`mode=ro`）+ `PRAGMA query_only=ON`。

### 8.4 语句切分

前后端各一份实现、同一套测试用例（JSON 文件共享）：识别单引号、双引号、反引号、`--`/`#`/`/* */` 注释、`/*! */` 条件注释、`DELIMITER` 指令、BEGIN...END 块（触发器、例程）。

### 8.5 结果编码

```json
{
  "columns": [{"name":"id","type":"BIGINT UNSIGNED","kind":"int","nullable":false,"table":"users","db":"app","pk":true}],
  "rows": [["1", "alice", null, {"$bin":"AQID","len":3}, {"$trunc":"前 64KB …","len":1048576}]],
  "has_more": true,
  "editable": {"ok": true, "db": "app", "table": "users", "key": ["id"]}
}
```

- 整数、小数一律以字符串传，防 JS 精度丢失；NULL 为 null；二进制为 `$bin`（base64，超过 64KB 截断带 len）；长文本超过 64KB 为 `$trunc`。
- kind：int / decimal / float / string / text / binary / date / datetime / time / year / bool / json / enum / set / geometry / other，前端据此选编辑器与对齐方式。
- 可编辑判断（§9.4）由服务端给出，不可编辑时 `editable.reason` 说明原因。

## 9. 表格编辑（变更集）

### 9.1 交互（对标 DataGrip）

- 单击选单元格，拖动/Shift 框选，Ctrl/Cmd 多选；方向键、Tab、Home/End、PageUp/PageDown 移动；直接打字或 F2、双击、回车开始编辑；Esc 取消；Ctrl+C 复制为 TSV（可选复制为 CSV、JSON、INSERT、UPDATE、Markdown、WHERE 条件）；Ctrl+V 从 Excel/表格粘贴多行多列（超出现有行时追加新行）；Delete 置空；Ctrl+Enter / 「提交」按钮提交；Ctrl+Z 撤销单元格改动；「撤销全部」。
- 改动显示：改过的单元格黄色底、新行绿色、待删除行红色删除线；行号栏显示状态。提交前可查看将执行的 SQL。
- 单元格右键：置为 NULL、置为默认值、复制、按此值筛选、查看值（值面板）、跳到外键引用的行、查看引用此行的行。
- 值面板：文本 / JSON（格式化与校验）/ 十六进制 / 图片预览（BLOB 是图片时）/ 日期选择；可加载完整值。
- 列：拖动调宽、点表头排序（服务端 ORDER BY）、隐藏列、冻结列；行号；转置视图（单行竖排）。
- 顶部：WHERE 输入框与 ORDER BY 输入框（带列名补全）、分页（100/500/1000/全部 ≤ 10 万）、精确总行数按需统计。

### 9.2 变更集接口

`POST /api/v1/connections/{cid}/data/apply`

```json
{"db":"app","table":"users","key":["id"],"dry_run":false,
 "changes":[
   {"op":"update","key":{"id":"1"},"set":{"name":"bob"},"old":{"name":"alice"}},
   {"op":"insert","values":{"name":"carol","age":"3"}},
   {"op":"delete","key":{"id":"9"}}]}
```

- 全部语句在一个事务里执行，任何一条失败整体回滚并返回失败语句序号与错误。
- update 带 `old`：生成 `WHERE key = ? AND col <=> old`（乐观并发，防覆盖别人刚改的值）；影响行数不是 1 时整体回滚并报告「该行已被修改或删除」。
- 参数化执行，值按列类型转换（字符串 → 数值、十六进制 → 二进制、"" 与 NULL 区分）。
- dry_run 只返回 SQL 文本。

### 9.3 键

主键；没有主键时用第一个非空唯一键；都没有时 SQLite 用 rowid（非 WITHOUT ROWID 表），MySQL 不可编辑。

### 9.4 查询结果可编辑

结果列全部来自同一张表（MySQL 用协议里的原始库表列名；SQLite 用 `sqlite3_column_table_name`），且包含完整的键列，且没有表达式列、聚合、DISTINCT、GROUP BY、UNION；否则只读并说明原因。

## 10. 其他管理功能

- 表结构设计器：列（名称、类型、长度/精度、无符号、可空、默认值、自增、字符集与排序规则、注释、位置拖动）、索引（普通/唯一/全文/空间、列顺序与前缀长度）、外键（引用表列、ON DELETE/UPDATE）、表选项（引擎、字符集、注释、自增起始）。按差异生成 ALTER，执行前预览；SQLite 不支持的修改按官方 12 步流程（建新表 → 复制 → 删旧 → 改名）生成。
- 导入导出：结果或整表导出 CSV、TSV、JSON、Excel(xlsx)、SQL INSERT、Markdown；CSV/TSV/Excel 导入到表（列映射、类型预览、出错策略、分批事务）；整库导出 / 导入 SQL 文件用自研导出导入器（§11）。大文件全部流式。
- 服务器监控（MySQL）：进程列表（自动刷新、按耗时排序、杀查询/杀连接）、全局状态（QPS、连接数、缓冲池命中率等曲线）、变量、表大小排行、慢查询（performance_schema 可用时）。SQLite：文件大小、页数、freelist、integrity_check。
- 用户与权限（MySQL）：账号列表、新建/删除/改密码、授权与回收（库、表、列级）、一键创建只读账号（SELECT、SHOW VIEW，可限定库）。
- ER 图：选库或若干表，按外键生成 Mermaid erDiagram，可导出 SVG/PNG。
- 查询历史（每人每连接，可搜索）、收藏的 SQL（脚本，可放文件夹，空间内共享）。

## 11. 自研 SQL 导出导入器（sqldump）

### 11.1 目标

- 导出文件与 Navicat「转储 SQL 文件」格式一致：可以在 Navicat「运行 SQL 文件」与官方 `mysql` 客户端导入。
- 导入器能导入：自己导出的、Navicat 导出的、mysqldump 导出的 SQL 文件。
- 可靠性第一：数据逐字节往返一致（§11.5 的测试是发布的前提）。

### 11.2 MySQL 格式

```sql
/*
 Navicat Premium Dump SQL            ← 实际写 "Lemi Dump SQL"，其余字段与 Navicat 相同

 Source Server         : <连接名>
 Source Server Type    : MySQL
 Source Server Version : 80036 (8.0.36)
 Source Host           : host:port
 Source Schema         : app

 Target Server Type    : MySQL
 Target Server Version : 80036 (8.0.36)
 File Encoding         : 65001

 Date: 26/11/2026 12:00:00
*/

SET NAMES utf8mb4;
SET FOREIGN_KEY_CHECKS = 0;
SET time_zone = '+00:00';            ← Navicat 没有这行。加上它，TIMESTAMP 列在不同时区的服务器之间往返才不会偏移

-- ----------------------------
-- Table structure for users
-- ----------------------------
DROP TABLE IF EXISTS `users`;
CREATE TABLE `users`  (
  `id` bigint UNSIGNED NOT NULL AUTO_INCREMENT,
  ...
  PRIMARY KEY (`id`) USING BTREE
) ENGINE = InnoDB AUTO_INCREMENT = 3 CHARACTER SET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci ROW_FORMAT = Dynamic;

-- ----------------------------
-- Records of users
-- ----------------------------
BEGIN;
INSERT INTO `users` (`id`, `name`) VALUES (1, 'alice');
COMMIT;

-- ----------------------------
-- View structure for v_users
-- ----------------------------
DROP VIEW IF EXISTS `v_users`;
CREATE ALGORITHM = UNDEFINED SQL SECURITY DEFINER VIEW `v_users` AS select ...;

-- ----------------------------
-- Procedure structure for p
-- ----------------------------
DROP PROCEDURE IF EXISTS `p`;
delimiter ;;
CREATE PROCEDURE `p`() ...
;;
delimiter ;

-- ----------------------------
-- Triggers structure for table users
-- ----------------------------
DROP TRIGGER IF EXISTS `trg`;
delimiter ;;
CREATE TRIGGER `trg` ...
;;
delimiter ;

SET FOREIGN_KEY_CHECKS = 1;
```

规则：

- 顺序：表（按名称）与数据 → 函数 → 存储过程 → 视图（按依赖拓扑排序；视图可能调用存储函数，所以放在例程之后）→ 事件 → 触发器。外键靠 FOREIGN_KEY_CHECKS=0。例程、触发器、事件按各自定义时的 sql_mode（事件还有 time_zone）重建。
- 一致性：导出在 `START TRANSACTION WITH CONSISTENT SNAPSHOT`（REPEATABLE READ）里读取全部数据；非 InnoDB 表给出提示。
- 值：整数原样；DECIMAL 原样文本；FLOAT/DOUBLE 用往返精确的最短表示；字符串单引号，按 MySQL 规则转义 `\0 \' \" \b \n \r \t \Z \\`；二进制/BLOB 一律 `0x` 十六进制（空值写 `''`）；BIT 写 `b'0101'`；日期时间按服务器返回的文本（含零日期与小数秒）；JSON 写字符串；GEOMETRY 写 `ST_GeomFromWKB(0x..., srid)`；生成列不导出数据，INSERT 写列名列表跳过它们。
- 选项：每条 INSERT 一行（Navicat 默认）或合并插入（Navicat「扩展插入」，默认每条 ≤ 1 MB）；只结构 / 只数据；选择表；是否包含 DROP。备份默认用合并插入（快几十倍），手动导出默认 Navicat 默认样式。
- 流式：边读边写，内存与表大小无关。导出时同时为每张表统计行数与内容哈希，写进备份清单（§12.3），供还原校验使用。

### 11.3 SQLite 格式

同样的注释头（Source Server Type : SQLite），`PRAGMA foreign_keys = false;` 开头，`PRAGMA foreign_keys = true;` 结尾；表结构用 `sqlite_master` 里的原始 SQL；数据 `BEGIN; INSERT ...; COMMIT;`；BLOB 写 `X'..'`；REAL 往返精确；自增表追加 `UPDATE "sqlite_sequence" SET seq = N WHERE name = 't';`；索引、视图、触发器随后。

### 11.4 导入器

- 流式切分（§8.4 同一套规则，额外支持 `DELIMITER`/`delimiter` 指令；`/*!40101 ... */` 条件注释原样发给服务器，由服务器按版本处理；mysqldump 的 `LOCK TABLES` 头尾）。文件读完时仍处在文件自己开的事务里（被截断）→ 回滚并报错。
- 执行：单连接；默认每 1000 条语句或 16 MB 提交一次（文件里有 BEGIN/COMMIT 时尊重文件）；可选「出错即停」（默认）或「跳过并记录」。
- 目标库可改名（文件中出现的 `USE`/库名限定按映射替换）。
- 进度：已读字节 / 总字节、已执行语句数、当前表；可取消。

### 11.5 测试（每次推送 CI 必跑）

- 真实服务器矩阵：MySQL 5.7、8.0、8.4、9.x 最新、MariaDB 10.11、11.x（GitHub Actions 服务容器）；SQLite 内置。
- 类型夹具：全部整数类型（有无符号、边界值）、DECIMAL(65,30)、FLOAT/DOUBLE（含 -0、极值、NaN 拒绝说明）、BIT(1..64)、CHAR/VARCHAR/TEXT 全系（含 `\0`、反斜杠、引号、换行、制表、`\Z`、emoji、各语言文字、超长）、BINARY/VARBINARY/BLOB（含全部 256 个字节值）、DATE/DATETIME(6)/TIMESTAMP(6)/TIME(6)（负值、>24 小时）/YEAR、零日期（宽松 sql_mode）、ENUM/SET、JSON（嵌套、unicode）、GEOMETRY/POINT/POLYGON（含 SRID）、生成列（虚拟、存储）、不可见列、自增、默认值表达式、各字符集（latin1、gbk、utf8mb4）、保留字与含反引号的标识符、表与列注释含特殊字符、外键环、视图嵌套、触发器、存储过程、函数、事件、分区表。
- 随机测试：随机生成表结构与数据（固定种子可复现），导出 → 导入新库 → 逐表比较 `SHOW CREATE TABLE`（规范化后）与按主键排序的逐行哈希。
- 交叉：我们的文件用官方 `mysql` 客户端导入后比较；mysqldump 的文件用我们的导入器导入后比较。
- 大表：100 万行导出导入，校验内存峰值与结果。
- 中断：导入中途取消、连接断开，必须报错而不是静默成功。

## 12. 备份

### 12.1 备份计划（每个连接可有多个）

- 范围：MySQL 选库（全部或勾选），SQLite 整个文件。
- 方式：MySQL —— `mysqldump`（与 databasus 一致：`--single-transaction --routines --triggers --events --hex-blob --set-gtid-purged=OFF`，5.7 服务器加 `--column-statistics=0`）或自研导出器（§11）；SQLite —— 自研导出器（SQL）或文件快照（`VACUUM INTO`，得到 .db 文件）。当前平台没有可用的 mysqldump 时（Win10 ARM），界面只给自研导出器并说明原因。
- 压缩：zstd（默认 5 级）/ gzip / 不压缩。
- 加密：可选，按备份计划设置备份密码（至少 12 位）。文件格式见 §12.3。密码同时用主密钥加密保存在 Lemi 里，以便自动还原；丢了 Lemi 数据目录时，用 `lemi backup-file` 加备份密码仍能离线解开。
- 存储目标：可选多个，依次上传（同一份数据流同时写多个目标）。
- 计划：每小时 / 每天 / 每周 / 每月 / cron，时区可选；失败自动重试 3 次（间隔 1、5、15 分钟）；同一连接同一时刻只跑一个备份；可手动立即备份。
- 保留：保留最近 N 份 / 保留 N 天 / GFS（时、日、周、月、年各保留几份，取并集）；只按成功的备份计数；新备份 1 小时内不删。
- 通知：成功、失败可分别勾选渠道。

### 12.2 执行

数据库 → （mysqldump 标准输出 / 自研导出器）→ 压缩 → 加密 → 逐目标流式上传，不落本地临时文件；同时计算 SHA-256 与大小。进度（已处理字节、当前表、速度）每秒推送。超时 23 小时。

### 12.3 文件格式

- 文件名：`<连接名>-<库名|all>-<UTC 时间 yyyyMMdd-HHmmss>.<sql|db>[.zst|.gz][.lemienc]`，存放路径 `<目标前缀>/<工作空间>/<连接 ID>/`。
- 清单：同名 `.manifest.json`（不加密）：备份 ID、方式、工具与版本、服务器版本、库清单、每张表行数与内容哈希（自研导出器）、压缩、是否加密、大小、SHA-256、开始结束时间。
- 加密格式 `.lemienc`（与 cryptstream 一致）：魔数 `LEMIENC1`，随后 KDF 参数（argon2id 盐、m、t、p）、随机 nonce 前缀，数据按 1 MiB 分块 AES-256-GCM，块序号与结尾标记写进 nonce/AAD，截断或篡改都会在解密时报错。

### 12.4 还原

- 选备份 → 选目标连接（同空间、同引擎、有写权限；可以是原连接或别的连接）→ 选库名映射 → 预检（目标库是否存在、是否为空，非空需勾选「覆盖」并输入库名确认）→ 执行。
- mysqldump 产物优先用对应版本的 `mysql` 客户端还原，也可以选自研导入器；自研导出产物用自研导入器；SQLite 文件快照直接替换文件（先备份原文件）。
- 还原记审计并通知。

### 12.5 还原校验

- 备份计划可开启「备份后校验」或按计划校验：MySQL 还原到空间里指定的校验连接上的临时库 `lemi_verify_<随机>`，逐表比对行数（自研导出器的备份还比对内容哈希），完成后删除临时库；SQLite 在数据目录临时文件里还原并 `PRAGMA integrity_check`。
- 校验结果记入备份记录，失败通知。

### 12.6 下载

- 原样下载（压缩、加密的原文件）或「下载为 .sql」（服务端解密解压后流式输出，文件就是可直接导入的 SQL）。
- 下载链接一次性、5 分钟有效。

### 12.7 存储目标

本地目录/NAS、SFTP（指纹确认）、WebDAV、FTP/FTPS、SMB、S3 兼容（AWS、R2、MinIO 等）、腾讯云 COS、阿里云 OSS、Azure Blob、百度网盘（扫码授权）。每个目标可以测试连接；被备份计划使用中的目标不能删除；按目标汇总占用（来自 Lemi 自己的记录）。

## 13. mysqldump 工具分发

- 来源：`.github/workflows/mysql-tools.yml` 手动触发，从 MySQL 官方 CDN 下载各平台归档 → 校验官方签名/摘要 → 只取 `mysqldump`、`mysql` 及其依赖的动态库（Linux 用 ldd、macOS 用 otool 自动收集）→ 打包成 `mysql-client-<版本>-<os>-<arch>.tar.gz|zip` → 计算 SHA-256 → 发布到本仓库 `tools-mysql-<版本>` Release，附 GPL 说明与对应源码下载地址。
- 平台：linux-amd64、linux-arm64、darwin-arm64、darwin-amd64、windows-amd64（windows-arm64 在 Win11 上仿真运行 windows-amd64 版本）。
- 版本：8.0（服务于 5.7 与 8.0 服务器）、8.4 LTS、9.7 LTS；9 及以后的服务器用同主版本里最新的客户端（创新版已改为年份版本号，如 26.x，需要时把对应版本加进工作流）。某个平台官方没有包时（例如 8.4、9.7 的最新版没有 Intel Mac 包），退回同系列里有包的版本。口令经临时 `--defaults-file`（0600，用完删除）传入，不读服务器上的 /etc/my.cnf 与 ~/.my.cnf。
- 清单 `lemi-service/internal/dumptools/manifest.json`（随代码发布）：每个 版本 × 平台 的文件名与 SHA-256。Lemi 下载 `<LEMI_TOOLS_BASE_URL>/<文件名>`，校验 SHA-256 后解压到 `<数据目录>/tools/mysql-<版本>/`。校验失败删除并报错。
- 也可以在设置里指定服务器上已安装的 mysqldump 路径（Lemi 读取其版本号）。Docker 镜像内预装。
- Docker 预装位置：`/data/tools` 会被数据卷覆盖，所以镜像构建时（docker/fetch-mysql-tools.sh，按本清单下载并校验 SHA-256）装在 `/opt/lemi/tools/mysql-<版本>/linux-<架构>/`（归档只有一个顶层目录时去掉这一层），镜像设置环境变量 `LEMI_TOOLS_PREINSTALLED=/opt/lemi/tools`。dumptools 查找某个版本时，先查 `$LEMI_TOOLS_PREINSTALLED/mysql-<版本>/<系统>-<架构>/`（只读，不下载、不删除），没有再查 `<数据目录>/tools/mysql-<版本>/`，都没有才下载。

## 14. 健康检查

- 每个连接可开关，默认间隔 60 秒、超时 10 秒、连续失败 3 次判定下线、成功 1 次判定恢复；记录保留 30 天。
- 检查：建立连接（经 SSH/代理）+ `SELECT 1`，记录延迟与错误。
- 只在状态变化时通知（下线、恢复）。
- 界面：每个连接的色块时间线（今天 / 7 天 / 30 天）、延迟曲线、可用率；连接列表上显示状态点。

## 15. 通知

- 渠道：邮件（SMTP，SSL/STARTTLS）、Webhook（GET/POST，自定义头，正文模板变量 `{{title}} {{message}} {{level}} {{link}} {{event}}`，可选 HMAC-SHA256 签名头 `X-Lemi-Signature`）、钉钉群机器人（可加签）、企业微信群机器人、飞书群机器人（可签名校验）。
- 事件：备份成功 / 失败、还原完成 / 失败、校验失败、连接下线 / 恢复、定时智能体任务结果、安全事件（登录锁定、新设备登录、管理员变更）。
- 渠道属于工作空间（安全事件用实例级渠道与用户个人渠道）；每个渠道可测试发送；发送失败重试 3 次并记录。

## 16. AI

### 16.1 供应商

- OpenCode Go（默认）：模型列表 `GET https://opencode.ai/zen/go/v1/models`，按每个模型的推荐协议与 base_url 调用；必带 `x-opencode-session`（会话 ID，ASCII）与自定义 User-Agent（`lemi/<版本>`）。
- 自定义供应商：名称、地址、密钥、协议（OpenAI Chat Completions / Anthropic Messages / OpenAI Responses）、模型列表（自动拉取或手填，含上下文长度、是否看图）。
- 供应商由实例管理员配置；工作空间可选择可用的模型。

### 16.2 智能体循环（移植 OpenCode 机制）

- 回合：用户消息 → 循环（组装上下文 → 流式请求 → 工具调用流出即开始执行，只读工具并发最多 4 个、写入按顺序 → 结果回灌）→ 直到本步没有工具调用。步数上限默认 200；最后一步注入「只总结已完成、未完成与下一步建议」的提示并禁止工具（工具定义保留，以免破坏缓存）。
- finish=length：注入续写提示（不要道歉、不要复述，把剩下的工作拆小），最多续两次。
- 插话（steer）：运行中用户发的消息在下一个步骤边界插入；也可选择排队到本轮结束后。
- 停止：先通知工具协作取消，5 秒后强制中断；未完成的工具记「已中断」。崩溃重启后把残留在运行中的工具与压缩标记为失败。
- 重试：错误先分类（上下文溢出 / 请求过大 / 内容策略 / 额度 / 鉴权 / 限流 / 服务端错误 / 无效请求 / 未知）。限流、服务端错误、未知、未送达的网络错误、流不完整可重试：2 秒起指数退避、单次封顶 10 秒、带抖动、最多 10 次；尊重 `retry-after-ms`、`retry-after`（封顶 15 分钟）与 `x-should-retry`；DNS/网络不可达单独计预算（最长 1 小时）。鉴权、额度、内容策略、无效请求不重试。上下文溢出不重试，直接触发压缩（每步最多一次）。已经输出一部分后断流：保存已有部分，开新的一步重试。
- 压缩：上限 = 窗口 − max(15% 窗口, 16k)；估算 = 上次实测用量 + 之后新增内容的估算。超过上限自动压缩：保留最近 15k token 原文，其余由模型按固定模板（目标 / 要求 / 决定 / 进展〔已完成 / 进行中 / 受阻〕/ 下一步 / 相关对象〔连接、库、表、结果集 ID、报告 ID〕/ 重要上下文）写成摘要，替换为 `<conversation-checkpoint>`；再次压缩时合并旧摘要。模型报上下文溢出时按 0.85 / 0.7 / 0.5 / 0.35 逐级缩小重试。
- 工具输出截断：超过 2000 行或 50 KB 时截断，完整内容存 `results/`，提示模型用 `read_result` 分页读取或交给子智能体处理。查询结果默认只给模型前 50 行（可要求到 500 行），完整结果存在服务端，图表与报告按结果 ID 引用，不经过模型 token。
- 坏的工具调用：未知工具名先尝试大小写/别名修正；参数 JSON 解析失败、缺字段、类型不对时按 schema 自动修复（字符串化的对象/数组解析、"20"→20、"false"→false、可选字段 null 删除、多余字段删除），修不好则以可读错误（列出问题并附原参数）回灌，让模型重发，不中断回合。
- 死循环检测：同一工具、同样参数连续 3 次 → 转为向用户确认。
- 权限答复：允许一次 / 本会话总是允许（按工具 + 连接 + 语句类别记忆，并自动放行其他已满足的挂起请求）/ 拒绝（带说明时作为工具错误回灌、循环继续；不带说明时结束本步）。
- 系统提示：基础提示（数据库分析师角色、工作方法、安全边界）+ 环境块（日期、时区、用户、工作空间、可用连接摘要与各自 AI 档位、当前打开的标签）+ 空间与个人自定义指令。会话开始时固定，之后的变化（换日、切换计划模式、连接列表变化）以 `<system-update>` 消息按时间插入，system 前缀不变以保住缓存。
- 子智能体（task 工具）：`explore`（只读，摸清结构与数据分布）、`analyst`（只读，完成一个独立的分析小任务并返回结论与结果 ID）。子智能体不能再派子智能体、不能问用户、不能写入；同步等待，结果以 `<task_result>` 返回；可用 task_id 续接。
- 计划清单：todo_write 全量覆盖（pending / in_progress / completed / cancelled），界面固定显示。
- 计划模式：只读分析，禁止一切写入与 DDL，进入与退出时插入说明。
- 标题：首条消息后台生成（3–8 字，与用户语言一致）。
- 撤销：本回合中智能体执行的写入记录在检查点里（受影响的语句与可生成的反向语句仅作为参考）；数据库写入不保证可撤销，所以写入一律按档位确认（§16.4）。

### 16.3 工具

| 工具 | 说明 | 只读 |
|---|---|---|
| list_connections | 可用连接、引擎、环境标签、AI 档位、健康状态 | 是 |
| list_databases / list_tables | 库、表（可按名称模式过滤，带行数估计、大小、注释） | 是 |
| describe_tables | 一次多张表：列、类型、可空、默认、注释、索引、外键、DDL | 是 |
| search_schema | 跨库按名称/注释搜表与列 | 是 |
| run_query | 只读查询；返回结果 ID、列、前 N 行、总行数（取满上限时标注）、耗时 | 是 |
| read_result | 按结果 ID 分页读取、只取某些列、或在服务端做简单排序过滤 | 是 |
| explain_query | 执行计划 | 是 |
| execute_sql | 写入（DML/DDL），按档位确认；返回影响行数；DDL 前自动附上相关表结构 | 否 |
| create_chart | 用结果 ID 或 SQL 生成图表（ECharts option 模板 + 数据映射），支持 2D 与 3D | 是 |
| create_diagram | Mermaid 图（ER、流程、时序） | 是 |
| er_diagram | 按外键为指定库/表生成 ER 图 | 是 |
| report_write / report_read | 创建或修改报告（Markdown + 图表/表格/图引用块） | 否（只改 Lemi 自己的数据） |
| workspace_open_table | 在主工作区打开表格页并设置 WHERE / ORDER BY / 列（浏览器端执行） | 是 |
| workspace_run_sql | 在主工作区控制台写入 SQL 并执行，结果显示在主表格（浏览器端执行，写语句仍按档位确认） | 视语句 |
| workspace_update_view | 修改当前标签的筛选、排序、列、定位到行 | 是 |
| workspace_read_view | 读取用户当前看的标签：连接、库表、筛选、选中的行与单元格 | 是 |
| list_backups / backup_status / health_status | 备份记录、计划、健康状态 | 是 |
| todo_write / task / ask_user | 计划清单、子智能体、向用户提问（选择题或填空） | 是 |

workspace_* 只在用户的浏览器在线时提供；定时任务与 MCP 没有这些工具。

### 16.4 AI 访问档位

- 每个连接设置「AI 档位」：不可见 / 只读 / 读写·逐条确认 / 读写·增删改自动（DDL 确认）/ 完全放开；以及库表白名单（`库.表` 通配，空 = 不限）。白名单外的对象在结构工具里不可见，查询引用时拒绝（解析 SQL 中的表引用，解析不了则拒绝）。
- 内置智能体用连接上的档位；每个 MCP 授权（OAuth 或访问令牌）在授权时单独选择每个连接的档位，不能高于连接上的档位。
- 实际权限 = min(用户对连接的权限, 连接 AI 档位, 授权档位, 会话模式〔计划模式 = 只读〕)。只读在数据库层强制（§8.3）。
- 「逐条确认」与 DDL 确认在内置智能体里弹审批卡片（显示完整 SQL、预计影响、所涉表）；MCP 客户端没有 Lemi 的审批界面，所以 MCP 授权只能选不可见 / 只读 / 读写·增删改自动 / 完全放开，由外部智能体自己的确认机制把关。
- 定时任务无人值守：需要确认的操作一律拒绝。
- 生产连接上的写入，即使档位允许自动，也在审批卡片里额外显示「生产」标记；完全放开档需要实例管理员允许。

### 16.5 工作区联动

浏览器经 WebSocket 接收服务端发来的 `client_call`（工具名、参数、调用 ID），在主工作区执行后回传结果。打开的标签在 AI 面板里有「来自 AI」标记；聊天里显示一张卡片（连接、表、条件、行数），点击切换到该标签。

#### 16.5.1 浏览器端工具约定

- 消息：服务端发 `{"t":"client_call","d":{"id","tool","args"}}`，浏览器回 `{"t":"client_result","d":{"id","result"}}`；工具不存在或抛异常时回 `{"id","error"}`。工具内部的业务错误放在 `result.error`（字符串），不抛异常。字段与服务端 §24.6 一致，下表是浏览器实际返回的完整字段（是 §24.6 的超集）。
- 实现：`lemi-console/src/modules/workspace-tools/`（按需加载）。标签组件把可读状态与动作登记在 `src/stores/tabstate.ts`（`registerTabState(tabId, getter, actions)`）：表格标签动作 `update({where?, order_by?, scroll_to_row?})`（等加载完成）与 `loaded()`；控制台标签动作 `run(sql)`。工具只读登记的状态，不直接碰组件内部。
- 权限：档位与白名单（§16.4）由服务端在发出 `client_call` 之前判定；`args.visible_connections` 是本次对智能体可见的连接 ID，浏览器拒绝打开、修改、读取其他连接的标签。浏览器以用户本人的会话执行，数据库层权限照常生效。
- 写语句（非 SELECT / WITH / SHOW / DESCRIBE / EXPLAIN / VALUES / TABLE / USE / 只读 PRAGMA）：服务端审批过（或档位允许自动执行）时参数带 `approved: true`；没带的，浏览器弹确认框（生产连接醒目提示），用户拒绝则返回 `{error}`。
- 行号：`selection.rows`、`goto_row` 用表格里显示的序号（从 1 开始，含翻页偏移）。
- 样本：纯文本（NULL 写作 `NULL`，二进制写作 `0x` 十六进制，单元格超过 500 字符截断并带 `…`）。`sample` 前 20 行；`workspace_read_view` 的 `max_rows` 默认 20、最多 100。
- 打开或修改过的标签标记 `fromAI`（标签栏显示 AI 标记）。

| 工具 | 参数 | 返回 |
|---|---|---|
| `workspace_open_table` | `connection_id`、`db`（SQLite 可省略，默认 `main`）、`table`、`where?`、`order_by?`、`columns?`（只看这些列）、`visible_connections?` | `{tab_id, connection_id, db, table, where, order_by, offset, rows_loaded, has_more, columns, sample}`；失败 `{error}` |
| `workspace_run_sql` | `connection_id`、`db?`、`sql`（可多条）、`title?`（标签标题）、`approved?`、`visible_connections?` | `{tab_id, connection_id, db, in_tx, results:[{kind: rows\|exec\|error, statement, columns, rows_loaded, affected, error, duration_ms, sample}]}` |
| `workspace_update_view` | `tab_id?`（缺省 = 当前标签，必须是表格）、`where?`、`order_by?`、`columns?`、`goto_row?`、`visible_connections?` | 同 `workspace_open_table` |
| `workspace_read_view` | `tab_id?`（缺省 = 当前标签）、`max_rows?`、`visible_connections?` | `{tab_id, kind, title, connection_id, db, table, where, order_by, sql, selection:{rows:[行号], columns:[列名], cells:[[行号, 列名, 值]]}, visible_rows_sample:{columns, rows}, total_loaded}`；表格另有 `offset`、`has_more`、`pending_changes`，控制台另有 `in_tx`、`results`；其他标签（DDL、设计器、ER 图、监控、账号）只给出类型、连接、库与对象名 |

- `workspace_open_table`：同一张表已有标签时激活它并只改条件，不开新标签；条件不变则只等当前加载完成。
- `workspace_run_sql`：每次新开一个控制台标签（会话独立），填入 SQL 后执行全部语句，结果显示在该标签的结果区。
- 改条件时如果表格有未提交的修改，不覆盖，返回 `{error}` 让智能体请用户先处理。

### 16.6 图表

- 前端 ECharts 6 + echarts-gl（3D 柱、3D 散点、曲面、3D 地球除外）。类型：柱、条形、折线、面积、饼/环、散点、气泡、热力、漏斗、雷达、桑基、树图、旭日、箱线、K 线、仪表盘、3D 柱、3D 散点、3D 曲面。
- `create_chart` 参数：`result_id` 或 `sql`、`option`（ECharts option JSON，数据通过 `dataset` 由服务端从结果集填充）、`encode` 映射。服务端清洗：只允许 JSON 值；去掉所有 `formatter` 里的 HTML、tooltip 强制 `renderMode: 'richText'`、去掉 `graphic` 里的外部图片地址；数据最多 5 万点。
- 图表保存为对象（ID），在聊天、报告中引用，可导出 PNG/SVG，可「重新执行查询刷新」。

### 16.7 报告

- 报告 = Markdown 正文 + 引用块（```lemi-chart <id>```、```lemi-table <result_id>```、```lemi-diagram <id>```）。数据在报告保存时做快照，之后不依赖数据库。
- 可保存、在工作空间内共享、生成公开链接（可设访问密码与有效期，实例管理员可整体关闭公开分享）、导出 HTML（单文件，图表为内嵌 SVG）/ PDF（浏览器打印样式）/ Markdown。

### 16.8 定时智能体任务

- 名称、计划（同备份）、提示词、使用的模型、可用连接（档位在任务里再收紧，默认只读）、通知渠道、产出（报告 + 摘要）。
- 每次运行是一段独立对话，可在界面查看完整过程；失败通知。

## 17. MCP

- 端点 `/mcp`（Streamable HTTP，2025-06-18 及以后协议版本）。鉴权：OAuth 2.1（动态注册、授权码 + PKCE、刷新令牌轮换与重用检测、撤销、资源与授权服务器元数据）或个人访问令牌（`lemi_pat_...`，只显示一次，存哈希，可设到期时间）。
- 授权页：选择工作空间、每个连接的档位（§16.4）、有效期。已授权应用在设置里可查看与吊销。
- 工具：§16.3 中除 workspace_*、todo_write、task、ask_user 之外的数据库、图表（返回图片与 option）、报告、备份状态工具。
- 设置页提供 Claude Code、Cursor、Codex、Claude Desktop 的配置片段一键复制。
- 校验 Host/Origin 防 DNS 重绑定；每个令牌限速（每分钟 120 次调用）。

## 18. 接口约定

- REST：`/api/v1/...`，JSON，UTF-8。错误：`{"error":{"code":"...","message":"..."}}`，HTTP 状态码表意。
- 实时：`/api/v1/ws`（WebSocket，会话 Cookie + Origin 校验）。消息 `{"t": 类型, "d": 数据}`：`health`、`backup_progress`、`run_event`（智能体事件）、`client_call` / `client_result`、`notice`。
- 智能体事件也可经 SSE `/api/v1/agent/runs/{id}/events?after=<seq>` 续传。
- 分页：`?cursor=&limit=`。时间：Unix 毫秒。ID：ULID。

## 19. 前端

- Vue 3.5 + Vite + TypeScript + Pinia + vue-router + vue-i18n（zh-CN、en）；无组件库，基础组件自写（reka-ui 做无样式交互底座），SVG 图标，界面专业克制。
- SQL 编辑器 CodeMirror 6（@codemirror/lang-sql，MySQL / SQLite 方言，按结构快照补全表、列、别名、关键字、函数）；格式化用 sql-formatter。
- 表格自写：虚拟滚动（行列都虚拟），固定行高，10 万行流畅；手机上单元格编辑走底部弹出编辑器，长按多选。
- 布局（电脑）：左侧连接与对象树，中间多标签主工作区（控制台 / 表格 / 表结构 / ER 图 / 监控 / 报告），右侧 AI 面板（可收起、可拖宽，挤压主区而不是覆盖），底部结果区可拖高。平板同电脑，面板改抽屉。手机：底部导航（连接 / 工作区 / AI / 备份 / 我的），对象树与标签列表为页面级导航，AI 全屏。
- 主题：浅色 / 深色 / 跟随系统；生产连接红色边框。

## 20. 打包与发布

- 推送 main 与 PR：前端构建、服务端 vet + 测试、sqldump 真实服务器矩阵测试、全平台交叉编译、gitleaks、govulncheck、npm audit（高危失败）。
- 推送 `v*` 标签：发布 Release `Lemi <版本>`：
  - `lemi-<版本>-linux-amd64.tar.gz`、`linux-arm64.tar.gz`、`darwin-arm64.tar.gz`、`darwin-amd64.tar.gz`、`windows-amd64.zip`、`windows-arm64.zip`（Windows 10/11，ARM64 原生）。
  - `SHA256SUMS` 与 `SHA256SUMS.sig`（Ed25519，私钥 Secrets `LEMI_RELEASE_SIGNING_KEY`，公钥编进程序，在线升级只接受签名正确的包）。
  - macOS 用固定的 Lemi 专用自签名代码签名证书签名（Secrets `MACOS_CERTIFICATE_P12` / `MACOS_CERTIFICATE_PASSWORD` / `MACOS_SIGN_IDENTITY`，私钥备份在维护者本机 `~/.lemi-secrets/`，永不更换；以后换 Developer ID 同名替换 Secrets 即可，并可加公证 Secrets）。
  - Docker 镜像 `ghcr.io/lemonitcn/lemi:<版本>` 与 `latest`（linux/amd64、linux/arm64，预装对应版本 mysqldump/mysql），仓库首页提供 docker-compose.yml。
- 纯 Go、关闭 CGO（SQLite 用 modernc），单文件内嵌前端。
- 在线升级：每天检查 GitHub 最新正式版，实例管理员一键升级（下载对应平台包 → 校验签名与 SHA-256 → 替换可执行文件 → 由服务管理器或自身重启）；Docker 部署只提示，不自升级。
  - 签名：`SHA256SUMS.sig` = Ed25519(私钥, SHA256SUMS 原始字节) 的标准 base64。清单里必须有「最新版本号」对应的包名（防止拿旧版本的真清单冒充新版本），不接受降级与同版本重装；替换前运行新程序的 `version` 确认版本号。签名工具 `go run ./internal/selfupdate/releasesign`（keygen / sign / verify / pubkey），sign 时私钥必须与内置公钥匹配。
  - 替换：同目录临时文件原子改名；Windows 运行中的 exe 先改名为 `.old`，新进程启动后清理。
  - 重启：Unix（前台或 systemd / launchd）给自己发 SIGTERM 正常退出后 execve 原地换成新程序（进程号不变）；Windows 前台正常退出后以相同参数启动新进程；Windows 服务拉起独立的 `lemi service restart` 进程经服务控制管理器停止再启动。
  - 接口：`GET /api/v1/admin/update`（当前 / 最新版本、发布说明、能否自动升级及原因、升级任务状态，`?refresh=1` 立即检查，最短间隔 1 分钟）、`POST /api/v1/admin/update`（实例管理员、需 re-auth、记审计 `system.update.start` / `system.update`，后台执行，返回 202，结果经 WebSocket `notice` 推送）；bootstrap 给实例管理员附带 `update: {version, url, can_auto_update}`。

## 21. 测试

- 服务端：单元测试 + 集成测试（SQLite 内置；MySQL 用 CI 服务容器，本地用 Docker）。安全相关（CSRF、会话、限速、只读强制、SQLite 路径逃逸、LOAD DATA LOCAL、权限组合）必须有测试。
- sqldump：§11.5。
- 前端：语句切分、结果编码解析、表格选择与粘贴逻辑、变更集生成用 vitest。
- 智能体：用假供应商回放事件流，覆盖重试、压缩、截断、修复、死循环、审批、停止。

## 22. 服务端包

```
internal/
  app         装配与生命周期
  config      配置（toml + 环境变量 + 参数）
  store       元数据 SQLite、迁移、快照
  crypt       主密钥、字段加密
  auth        密码、TOTP、会话、限速、re-auth
  accounts    用户、工作空间、成员、权限计算
  audit       审计
  web         HTTP、中间件、各模块接口挂载、静态前端
  realtime    WebSocket
  netsafe     出站地址策略
  netdial     SSH 隧道、SOCKS5、HTTP 代理拨号
  driver      驱动接口与注册表；driver/mysql、driver/sqlite
  sqlsplit    语句切分
  sqlclass    语句分类（只读判断、表引用解析）
  conns       连接配置、连接池、会话、执行、变更集
  sqldump     自研导出导入器
  dumptools   mysqldump 下载与调用
  backup      计划、执行、保留、还原、校验、下载；backup/target 存储目标；backup/cryptstream
  health      健康检查
  notify      通知渠道
  llm         模型供应商
  agent       智能体循环
  tools       工具注册表（智能体与 MCP 共用）
  charts      图表清洗与存储
  reports     报告
  tasks       定时智能体任务
  mcpserver   MCP
  oauth       MCP 的 OAuth 2.1
  sched       计划表达式（每小时/天/周/月/cron）
  selfupdate  在线升级
  version     版本
  logx        日志
```

## 23. 备份/健康检查/通知接口

全部在 `/api/v1` 下，需要登录（会话 + CSRF，见 §5.2、§18）。权限列：viewer / member / admin 指空间角色（§4.2），「写」「读」指对该连接的有效权限（§4.2 的个人收紧与连接只读都计入）；「实例管理员」指实例级 admin。所有带 `{ws}` 的接口都校验对象确实属于该空间，不属于一律 404；对连接被收紧为 none 的成员，相关计划、记录、还原同样不可见（连接已删除时只有空间 admin 可见）。密钥字段一律只写不读：视图只给 `secrets_set`（已设置的密钥字段名），更新时不传表示保持原值，主机/地址类字段变了必须重新输入（§5.3）。

### 23.1 备份（`/workspaces/{ws}/backups`）

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/kinds` | viewer | 存储目标类型与表单字段 `{kinds:[{id,name,name_en,fields:[{key,label,label_en,type,required,secret,default,placeholder,help,options}],admin_only,network,oauth}], is_instance_admin}`；顺序 local、sftp、webdav、ftp、smb、s3、cos、oss、azure、baidu |
| GET | `/methods?connection=` | viewer + 读 | 该连接可用的备份方式 `{methods:[{id,available,default,reason,warnings}]}`。MySQL：mysqldump（本平台没有可用 mysqldump 时 available=false 并说明原因，例如 Win10 ARM）、lemi；MariaDB 默认 lemi 并给出警告。SQLite：sqlite_file（默认）、lemi |
| GET / PUT | `/settings` | viewer / admin | 空间备份设置 `{verify_connection_id}`：还原校验用的 MySQL 连接（必须本空间、非只读） |
| GET | `/targets` | viewer | `{targets:[{id,name,kind,created_at,updated_at,usage:{files,bytes},config?,secrets_set?}]}`；`config` 只给空间 admin |
| POST | `/targets` | admin（local 类型要求实例管理员） | `{name,kind,config:{...}}`，只校验配置、不连网 |
| POST | `/targets/test` | admin | 用未保存的表单测试 `{id?,name,kind,config,clear?}`；带 id 时沿用已保存的密钥 |
| GET / PUT / DELETE | `/targets/{id}` | viewer / admin / admin | 不能改类型；`clear:[字段]` 显式清除可选密钥；被计划使用时删除返回 409（目标上已有的备份文件不删） |
| POST | `/targets/{id}/test` | admin | 写、查、列、读回比对、删一个 `lemi-probe-*` 探测文件。SFTP 首次连接返回 409 `sftp_host_key_unknown`（extra：fingerprint、key_type），指纹变化返回 409 `sftp_host_key_mismatch` |
| POST | `/targets/{id}/host-key` | admin | 确认 SFTP 主机指纹 `{fingerprint:"SHA256:..."}` |
| GET | `/targets/{id}/usage?actual=1` | viewer | `{recorded:{files,bytes}, actual?:{files,bytes}}`：recorded 来自 Lemi 自己的记录；actual=1 时实际列目录统计 |
| POST | `/targets/{id}/baidu/device` | admin | 百度网盘设备码授权第一步，返回 `{device_code,user_code,verification_url,qrcode_url,expires_in,interval}` |
| POST | `/targets/{id}/baidu/poll` | admin | `{device_code}` → `{authorized:false,pending:true,slow_down}` 或 `{authorized:true}`（令牌加密写回配置） |
| GET | `/plans?connection=` | viewer | `{plans:[计划视图 + busy]}` |
| POST | `/plans` | admin | 见下方计划字段 |
| GET / PUT / DELETE | `/plans/{id}` | viewer / admin / admin | 不能改所属连接；删除计划不删已有备份 |
| POST | `/plans/{id}/run` | member + 写 | 立即备份，返回运行记录（异步执行）；同一连接正在备份/还原/校验时 409 |
| GET | `/runs?plan=&connection=&status=&cursor=&limit=` | viewer | `{runs:[...], next_cursor}`，按时间倒序，cursor 为上一页最后一条的 id，limit 默认 50、最多 200 |
| GET | `/runs/{id}` | viewer | 运行记录 + `manifest`、`files:[{target_id,path,size,status(ok/failed/deleted),error}]`、`verify`（校验详情），运行中时带 `progress` |
| DELETE | `/runs/{id}` | admin | 删除各目标上的文件与清单，再删记录（任一目标删除失败则保留记录） |
| POST | `/runs/{id}/cancel` | member + 写 | 取消运行中的备份 |
| POST | `/runs/{id}/download` | viewer + 读 | `{format:"raw"\|"sql", target_id?}` → `{url:"/api/v1/downloads/<令牌>", filename}`；raw 原样（压缩、加密的原文件）；sql 服务端解密解压流式输出（SQL 备份得到 .sql，SQLite 文件快照得到 .db）；令牌一次性、5 分钟、只限本人；记审计 |
| POST | `/runs/{id}/verify` | member | 立即做还原校验，返回 `{task_id}` |
| POST | `/runs/{id}/restore/precheck` | member + 对目标连接写 | `{connection_id?, database_map?, method?}` → `{items:[{source,target,exists,tables,need_confirm,confirm_text}], method, method_error?}` |
| POST | `/runs/{id}/restore` | member + 对目标连接写 | `{connection_id?, database_map?:{备份里的库:目标库}, method?:"mysql"\|"lemi"\|"sqlite_file", target_id?, confirm?:{目标库:输入的文字}}`；目标非空且没有正确确认时 409 `restore_confirm_required`（extra 为预检结果，SQLite 要输入连接名）；返回还原记录（异步） |
| GET | `/restores?cursor=&limit=`、`/restores/{id}` | viewer | 还原记录 `{id,run_id,target_id,connection_id,connection_name,database_map,method,status,error,result,created_by,started_at,finished_at,progress?}` |
| POST | `/tasks/{id}/cancel` | member + 写 | 取消还原或校验任务（id 为还原记录 id 或校验 task_id） |

计划字段（POST/PUT `/plans` 请求，视图同名返回）：`connection_id`、`name`、`enabled`、`method`（mysqldump | lemi | sqlite_file）、`databases`（MySQL 选的库，空 = 全部用户库）、`compression`（zstd 默认 5 级 | gzip 默认 6 级 | none）、`compression_level`、`encrypt`、`passphrase`（只写，至少 12 位；视图只给 `passphrase_set`）、`targets`（目标 ID）、`schedule`（sched.Spec：`{kind:hourly|daily|weekly|monthly|cron|manual, minute, time, weekdays, day, cron, timezone}`）、`retention`（`{mode:""|count|days|gfs, count, days, hourly, daily, weekly, monthly, yearly}`）、`verify`（`{after_backup:bool, schedule?:Spec}`）、`notify_success` / `notify_failure`（通知渠道 ID）。视图另有 `schedule_text`、`next_run_at`、`retry_at`、`retry_attempt`、`next_verify_at`。

运行记录字段：`id, plan_id, plan_name, connection_id, connection_name, driver, trigger(schedule|manual|retry), attempt, status(running|success|failed|canceled), method, tool, server_version, databases, file_name, compression, encrypted, size, raw_size, sha256, warnings, error, created_by, started_at, finished_at, verify_status(""|running|ok|failed), verify_at`。

执行规则：同一连接同一时刻只跑一个任务（备份、还原、校验共用）；全局并发 3；超时 23 小时；计划触发的失败自动重试 3 次（1、5、15 分钟后），每次失败都发失败通知；启动时残留的「运行中」记为失败（服务重启中断）。多个目标同时写同一份数据流，某个目标失败时放弃它、其余继续，至少一个目标成功即为成功（失败的目标记在 files 与 warnings 里）。必须预先知道大小的目标（百度网盘、勾选「先暂存」的 WebDAV）会暂存到 `<数据目录>/tmp/backup/spool/`，SQLite 文件快照先 `VACUUM INTO` 到 `<数据目录>/tmp/backup/snapshot/`，用完即删。mysqldump 的口令经临时选项文件（0600，`--defaults-file`，用完删除）传入，不进命令行；除「直连 + verify-full」外一律经 127.0.0.1 随机端口的临时转发连数据库（SSH 隧道、代理与网络策略都在转发的拨号里执行），经转发时 verify-full 降为只校验 CA 并给出警告。

备份清单 `<文件名>.manifest.json`（不加密）：`{format:"lemi-backup-manifest/1", backup_id, plan_id, workspace_id, connection_id, connection_name, driver, method, tool, server_product, server_version, databases:[{Name, Tables:[{Name,Rows,Hash}], Views, Routines, Triggers, Events}], file, compression, compression_level, encrypted, encryption, size, raw_size, sha256, started_at, finished_at, warnings, hash_algorithm}`。mysqldump 备份的每表行数由 Lemi 旁观输出流统计（没有内容哈希）；自研导出器与 SQLite 文件快照还有内容哈希（与 `sqldump.TableChecksum` 相同）。

还原校验：MySQL 备份还原到校验连接上的临时库 `lemi_verify_<随机>`（多库时加 `_<序号>`），mysqldump 备份用 mysql 客户端（按映射改写 `CREATE DATABASE` 与 `USE` 行），自研备份用自研导入器；逐表比对行数（有哈希时比对哈希），检查缺表与多表，结束删除临时库。SQLite 在 `<数据目录>/tmp/backup/verify/` 的临时文件里还原，`PRAGMA integrity_check` 后逐表比对。结果写进运行记录的 `verify`：`{ok, method, connection, temp_databases, tables_checked, rows_checked, hash_checked, integrity, mismatches:[{database,table,expected_rows,got_rows,expected_hash,got_hash,error}], error, duration_ms, temp_db_dropped}`；失败时发 `verify_failed` 到计划的失败渠道。

还原：mysqldump 备份默认用 mysql 客户端（库名映射同上改写），也可选自研导入器；自研备份只能用自研导入器（映射作用于 CREATE/DROP/ALTER DATABASE 与 USE）。SQLite 文件快照还原：先写到数据库所在目录的临时文件并做完整性检查，关闭该连接的连接池与控制台会话，原文件（连同 -wal/-shm）改名为 `<文件>.before-restore-<UTC 时间>` 保留，再把临时文件改成正式名；还原记录的 `result.previous_file` 给出保留的文件名。还原成功/失败发 `restore_done` / `restore_failed` 到计划的成功/失败渠道，并记审计。

### 23.2 mysqldump 工具（实例管理员）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/admin/dumptools` | `{platform, platform_error?, versions:[{version,series,available,installed,file,size_bytes}], custom:{mysqldump_path,mysql_path}, custom_version?, custom_error?, base_url}` |
| POST | `/admin/dumptools/{version}/install` | 下载 `<LEMI_TOOLS_BASE_URL>/<tag>/<文件名>`，校验 SHA-256，安全解压（拒绝路径穿越、链接、超大与过多文件）到 `<数据目录>/tools/mysql-<版本>/<os-arch>/`；失败删除。备份或还原需要时也会自动安装 |
| DELETE | `/admin/dumptools/{version}` | 删除已安装的版本 |
| PUT | `/admin/dumptools/custom` | `{mysqldump_path, mysql_path}`：指定服务器上已安装的客户端（读取 `--version`；两项都为空表示取消）。会被执行，所以要求 10 分钟内验证过身份（re-auth） |

查找顺序：系统设置里指定的路径 → `<数据目录>/tools` 里下载的 → 环境变量 `LEMI_TOOLS_PREINSTALLED` 指向的只读预装目录（Docker 镜像为 `/opt/lemi/tools`，布局相同）→ 自动下载。工具包布局（`scripts/extract-mysql-tools.sh` 产出）：没有顶层目录，`bin/mysqldump`、`bin/mysql`（Windows 为 .exe，DLL 也在 bin/）、`lib/<依赖库>`、`LICENSE`、`README-lemi.txt`。服务器版本 → 客户端：MySQL 5.x 与 8.0 用 8.0 客户端（5.7 加 `--column-statistics=0`），8.1–8.4 用 8.4，9.x 及以后用清单里同主版本最新的；清单缺对应系列（或该系列缺本平台的包）时用不低于它的最老可用版本并给出警告。MariaDB 用 8.0 客户端加 `--column-statistics=0`，并建议改用自研导出器。mysqldump 参数：`--single-transaction --quick --routines --triggers --events --hex-blob --default-character-set=utf8mb4 --max-allowed-packet=1G --no-tablespaces --set-gtid-purged=OFF --databases <库…>`。

### 23.3 健康检查（`/workspaces/{ws}/health`）

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/` | viewer | 当前用户能看到的连接的状态 `{connections:[{connection_id,name,driver,env,enabled,interval_sec,status(unknown|up|down|disabled),fails,last_check_at,last_ok_at,last_latency_ms,last_error,changed_at}]}` |
| GET | `/{cid}/config` | viewer + 读 | `{connection_id, custom, config:{enabled,interval_sec,timeout_sec,fail_threshold,channels}, defaults}`；没有配置行的连接按默认值启用（60 秒、10 秒、连续 3 次失败下线、1 次成功恢复） |
| PUT | `/{cid}/config` | admin | 请求为 config 对象：interval_sec 10–86400，timeout_sec 1–120 且不大于间隔，fail_threshold 1–100，channels 必须是本空间的通知渠道；关闭时状态回到 unknown |
| GET | `/{cid}/timeline?range=day\|7d\|30d&tz=` | viewer + 读 | `{range, step(hour|day), tz, interval_sec, buckets:[{start,end,checks,failures,uptime|null,avg_latency_ms,p95_latency_ms,max_latency_ms,status(none|up|down|partial)}], summary}`；day = 今天按小时，7d/30d = 含今天的自然日；延迟只统计成功的检查 |
| GET | `/{cid}/checks?cursor=&limit=&failed=1` | viewer + 读 | 原始检查记录 `{checks:[{id,at,ok,latency_ms,error,status,manual}], next_cursor}`，limit 默认 100、最多 500 |
| POST | `/{cid}/check` | member + 读 | 立即检查（同一连接的并发请求共享一次检查），返回检查结果 |

实时消息 `health`（推给空间成员，排除对该连接被收紧为 none 的人）：`{workspace_id, connection_id, at, ok, latency_ms, error, status, prev, changed, fails, manual}`。只在状态变化（下线、恢复）时发 `conn_down` / `conn_up` 通知；检查记录保留 30 天。

### 23.4 通知（`/workspaces/{ws}/notify`）与系统邮件

| 方法 | 路径 | 权限 | 说明 |
|---|---|---|---|
| GET | `/kinds` | viewer | `{kinds:[{id,name,name_en,help,fields:[...,show_if]}], events:[{id,name,name_en}], system_smtp:bool}`；渠道类型 email、webhook、dingtalk、wecom、feishu |
| GET / POST | `/channels` | viewer / admin | 视图 `{id,workspace_id,name,kind,enabled,created_by,created_at,updated_at}`，空间 admin 另有 `config`（不含密钥）、`secrets_set`、`url_host`；POST 请求 `{name,kind,enabled?,config,clear?}` |
| GET / PUT / DELETE | `/channels/{id}` | viewer / admin / admin | 删除渠道后发送记录保留 |
| POST | `/channels/{id}/test` | admin | 同步发一次（不重试，结果也记录），成功 `{ok:true,latency_ms}`，失败 400 `notify_failed` |
| POST | `/channels/test` | admin | 用未保存的表单测试 `{id?,name,kind,config,clear?}` |
| GET | `/logs?cursor=&limit=&channel=` | viewer | `{logs:[{id,channel_id,channel_name,kind,event,level,title,message,status(sending|ok|failed),attempts,error,created_at,finished_at}], next_cursor}` |
| GET | `/api/v1/admin/smtp` | 实例管理员 | 系统邮件服务器 `{configured,host,port,tls,username,from,secrets_set}` |
| PUT | `/api/v1/admin/smtp` | 实例管理员 + re-auth | `{host,port,tls(ssl|starttls|none),username,password?,from,clear?}`；host 为空表示清空。首次启动时若从未保存过且设置了 LEMI_SMTP_*，按环境变量写入 |
| POST | `/api/v1/admin/smtp/test` | 实例管理员 | `{to?, config?}`，to 不填发给管理员本人 |

事件：`backup_success`、`backup_failed`、`restore_done`、`restore_failed`、`verify_failed`、`conn_down`、`conn_up`、`task_result`、`security`、`test`。发送失败重试 3 次（2、10、30 秒后；408/429 以外的 4xx、地址被拦截、配置错误不重试），每次发送写进记录，保留 30 天。工作空间渠道的出站一律按「允许访问内网」设置检查（DNS 解析后按实际 IP，云元数据地址永远拒绝），设置改动立即生效；实例管理员配置的系统邮件服务器允许内网（仍拒绝云元数据地址）。邮件加密方式 `none` 只用于不带口令的内网中继（不加密的连接拒绝发送口令）。

Webhook 签名：`X-Lemi-Timestamp`（Unix 秒）、`X-Lemi-Signature: sha256=<hex(HMAC-SHA256(密钥, 时间戳 + "." + 正文))>`（GET 的「正文」为完整查询串），另带 `X-Lemi-Event`；接收方应做常量时间比较并拒绝与当前时间相差 5 分钟以上的请求。正文模板变量 `{{title}} {{message}} {{level}} {{link}} {{event}}` 按 Content-Type 转义。钉钉加签：`base64(HmacSHA256(key=secret, data=毫秒时间戳 + "\n" + secret))` 再 urlencode，拼 `&timestamp=&sign=`；飞书：`base64(HmacSHA256(key=秒时间戳 + "\n" + secret, data=""))`，放在请求体的 `timestamp`、`sign`；企业微信靠地址里的 key。钉钉、企业微信、飞书按响应体的 errcode/code 判定成败。

安全事件（`account_locked`、`new_device_login`、`password_changed`、`totp_disabled`）用系统邮件服务器发给用户本人，内容含时间、IP、UA；用户没有邮箱或没有配置系统邮件服务器时只记日志。

智能体与 MCP 的 `list_backups`、`backup_status`、`health_status` 工具由备份服务实现（`tools.BackupInfo`），只返回调用方可见连接的数据，不含任何密钥或备份密码。

### 23.5 实时消息 `backup_progress`

每秒推给空间成员一次，结束时再推一次带 `done:true` 与最终 `status`、`error` 的消息：`{kind(backup|restore|verify), id(运行记录 / 还原记录 / 校验任务 ID), workspace_id, connection_id, plan_id, phase(queued|preparing_tool|downloading_tool|running|uploading|comparing), bytes(已处理的原始数据字节), written(写到目标或从目标读的字节), total, rows, database, table, tables_done, tables_total, statements, errors, speed(字节/秒), elapsed_ms}`。

### 23.6 离线工具

`lemi backup-file decrypt <文件> [-o 输出] [--passphrase-file 文件]` 解密 .lemienc 得到压缩后的原文件；`lemi backup-file to-sql <文件> [...]` 解密（需要时）并按文件头识别 zstd/gzip 解压，得到 .sql（SQLite 文件快照得到 .db）。不需要 Lemi 数据目录；没给口令文件时在终端无回显输入；`-o -` 输出到标准输出；默认写在原文件旁边，不覆盖已有文件，出错时不留下不完整的输出文件。

## 24. 智能体 / MCP / 报告接口

本节是 §16、§17 的落地细节：接口、事件、浏览器端工具的参数与返回、权限的具体规则。前端照这里实现。所有 `/api/v1` 接口沿用 §18 的约定（JSON、错误格式、CSRF 头）；时间为 Unix 毫秒，ID 为 26 位小写 ULID。

### 24.1 服务端包与数据

| 包 | 作用 |
|---|---|
| `llm` | 消息/工具/流事件模型；Chat Completions、Anthropic Messages、OpenAI Responses 三种协议；OpenCode Go 与自定义供应商；错误分类（`Classify` → `ErrorKind`）与 `retry-after-ms` / `retry-after` / `x-should-retry` 解析；流没有正常结束标记就断开时报 `stream_incomplete` |
| `results` | 大结果集：`<数据目录>/results/<id>.json` + 表 `ai_results`，7 天清理；查询结果（最多 10 万行、64 MB）与被截断的长工具输出（文本） |
| `charts` | 图表（ECharts option 模板 + dataset 快照）与图（Mermaid）对象，option 清洗 |
| `reports` | 报告、引用块快照、公开链接、Markdown 导出 |
| `tools` | 工具注册表（内置智能体、子智能体、定时任务、MCP 共用）、AI 档位权限计算、参数修复、输出截断 |
| `agent` | agent loop、压缩、子智能体、会话存储、事件流、模型供应商配置 |
| `oauth` | MCP 的 OAuth 2.1 授权服务器与个人访问令牌 |
| `mcpserver` | `/mcp` 端点 |
| `tasks` | 定时智能体任务 |
| `aitest` | 测试夹具（只在测试里引用） |

迁移：`300_ai.sql`（供应商、会话、消息、运行、结果、图表、报告）、`310_mcp_tasks.sql`（OAuth 客户端、授权码、令牌、授权、定时任务与运行记录）。

装配（`app/mod_ai.go`，Order 300）：备份模块若 `a.Set("backup", x)` 且 `x` 实现 `tools.BackupInfo`，就注册 `list_backups` / `backup_status` / `health_status`；通知模块若 `a.Set("notify", x)` 且实现 `tasks.TaskNotifier`，定时任务结果经它发出，否则只记日志。

```go
// tools.BackupInfo：connIDs 是本次调用对智能体可见的连接（已按权限过滤），返回值会序列化成 JSON 给模型
AIListBackups(ctx, u *accounts.User, workspaceID string, connIDs []string, limit int) (any, error)
AIBackupStatus(ctx, u *accounts.User, workspaceID string, connIDs []string) (any, error)
AIHealthStatus(ctx, u *accounts.User, workspaceID string, connIDs []string) (any, error)

// tasks.TaskNotifier
NotifyTask(ctx, n tasks.TaskNotification) error
// TaskNotification：workspace_id, channel_ids, task_id, task_name, run_id, status(done|error|stopped), level(info|error),
// title, message, summary, error, report_ids, link, started_at, finished_at
```

### 24.2 权限规则（§16.4 的实现）

- 实际档位 `EffectiveAI = min(用户对连接的权限, 连接 AI 档位, 授权档位, 会话模式)`：
  - 用户权限 none → 不可见；read（查看者、个人收紧为只读）→ 最多只读；连接设为只读 → 最多只读；
  - 连接档位为「完全放开」但实例管理员没打开 `AllowAIFullAccess` → 按「增删改自动」；
  - 授权（MCP 授权、定时任务的连接设置）里没列出的连接 → 不可见；
  - 计划模式、子智能体 → 最多只读；连接不属于当前工作空间 → 不可见。
- 不可见的连接在任何工具里都不出现，引用它与引用不存在的连接报同样的错。
- 库表白名单（`库.表`，`*`/`?` 通配，SQLite 的库是 `main`）：结构工具只列出白名单内的库与表；查询与写入用语句分类器解析表引用（排除 WITH 定义的公共表表达式名、`EXTRACT(… FROM 列)` 一类函数参数），任何一张表不在白名单 → 拒绝；表引用解析不完整（`TablesComplete=false`）→ 拒绝；有白名单时 `SHOW` / `PRAGMA` 一律拒绝（用结构工具代替），直接查 `information_schema` 拒绝；多语句里的 `USE` 切换到的库也必须在白名单里。
- 只读查询（`run_query`、`explain_query`、`create_chart` 的 SQL）一律经 `conns.OpenConsole(..., forceReadOnly=true, timeout=AgentTimeoutSec)`，数据库层强制只读；只接受一条 `Read` 类语句。
- 写入（`execute_sql`、`workspace_run_sql` 里的写语句）在 `conns.Exec` 的 `Check` 钩子里逐条判断：
  - 档位低于「读写·逐条确认」→ 拒绝；计划模式 → 拒绝；
  - 「逐条确认」→ 每条写语句都要审批；
  - 「增删改自动」→ 没有危险标记的 INSERT/UPDATE/DELETE 自动；DDL、管理语句、CALL、没有 WHERE 的 UPDATE/DELETE 要审批；
  - 「完全放开」→ 全自动；
  - 事务控制、`USE`、只读语句不需要审批。
- 需要审批时按调用方处理：内置智能体弹审批卡片（§24.4 `approval` 事件）；MCP 由外部智能体自己的确认机制把关（自动放行），但档位落到「逐条确认」时拒绝写入（MCP 授权本身不能选「逐条确认」，§24.7）；定时任务一律拒绝。
- 审批卡片字段：`tool, key, connection_id, connection_name, driver, production(env=prod), db, sql, class(dml|ddl|admin|call|other|session), keyword, tables, dangerous, reason, estimate（MySQL 增删改用 EXPLAIN 估算扫描行数）, related_ddl（{库.表: 建表语句}）, ai_level, explanation（智能体写的 reason）`。
- 审计：每条执行的 SQL 由 `conns.Exec` 记审计，来源 `agent` / `mcp` / `task`；图表创建、报告创建修改、授权与令牌变化另有审计。

### 24.3 智能体会话接口

| 方法与路径 | 说明 |
|---|---|
| `GET /workspaces/{ws}/ai/models` | 空间可用模型 `{configured, models:[{ref, id, name, provider_id, provider_name, context, max_output, vision, efforts, default_effort, api, featured}], default, errors}`（查看者及以上）；`?all=1` 返回实例全部可用模型、不按空间设置过滤（空间 AI 设置页选模型用） |
| `GET/PUT /workspaces/{ws}/ai/settings` | 空间 AI 设置 `{models:[ref], default_model, instructions}`；`models` 为空表示全部可用；PUT 要空间管理员 |
| `GET/PUT /me/ai` | 个人自定义指令 `{instructions}`（≤ 8000 字） |
| `GET /workspaces/{ws}/agent/conversations?kind=chat\|task&archived=1&limit=` | 我的会话列表（成员及以上） |
| `POST /workspaces/{ws}/agent/conversations` | 新建 `{settings:{model, effort, mode}}` → `{conversation}` |
| `GET /agent/conversations/{id}` | `{conversation, messages, todos, summary, summary_upto, context, active_run?:{id, state, resume_from, pending}}`；消息里隐藏片段（系统更新、续写提示）与思考签名已去掉 |
| `PATCH /agent/conversations/{id}` | `{title?, archived?, settings?}`；运行中修改模式会在下一个步骤边界生效并插入 `<system-update>` |
| `DELETE /agent/conversations/{id}` | 删除（先停止运行，子智能体会话一起删） |
| `POST /agent/conversations/{id}/messages` | 发消息 `SendInput` → `{run_id, message?, steered?, queued?}` |
| `GET /agent/runs/{id}` | 运行状态 `{id, conversation_id, state, last_seq, resume_from, pending}`；已结束很久的从库里读 `{state, error, steps, started_at, finished_at}` |
| `GET /agent/runs/{id}/events?after=<seq>` | SSE 续传（也认 `Last-Event-ID`），每条 `id: <seq>`、`data: <Event JSON>`，15 秒心跳注释；运行结束且发完后关闭 |
| `POST /agent/runs/{id}/stop` | 停止：先取消工具（数据库查询随之取消），5 秒后强制中断，未完成的工具记「已中断」 |
| `POST /agent/runs/{id}/respond` | 回应交互 `{id, reply:"once"\|"always"\|"reject", message?, answers?}` |
| `GET /agent/results/{id}?offset=&limit=` | 结果集分页（自己的结果；`limit` ≤ 5000）`{result, columns, rows, total}`；文本结果 `{result, text, total}` |
| `GET /charts/{id}` | 图表/图 `{chart:{id, kind:"echarts"\|"mermaid", title, option?, gl?, rows?, truncated?, refreshed_at?, source?, code?, diagram?, refreshable}}`（空间查看者） |
| `POST /charts/{id}/refresh` | 重新执行查询刷新数据快照（成员；按当前用户自己的连接权限在只读会话里执行） |

`SendInput`：

```json
{"text": "…", "delivery": "steer|queue", "model": "<供应商ID>/<模型ID>", "effort": "low", "mode": "normal|plan",
 "context": {"open_tabs": [{"id":"t1","kind":"table","connection_id":"…","db":"app","table":"orders","title":"orders"}], "active_tab": "t1"}}
```

- 会话空闲：写入用户消息并开始一轮运行。正在运行：`delivery=steer`（默认）在下一个步骤边界插入；`queue` 排队到本轮结束后自动开始下一轮（旧运行推 `next_run` 事件）。
- 首条消息时固定系统提示（基础提示 + 环境块 + 空间约定 + 个人指令）；之后日期、模式、连接列表、打开的标签变化以隐藏的 `<system-update>` 用户消息插入，系统提示不变。
- 首条消息先用前 30 个字作临时标题，后台让模型生成 3–8 字标题（`title` 事件）。

消息片段（`messages[].parts[]`）：`{type: text|reasoning|tool_call|tool_result, text, call_id, name, args, is_error, title（工具中文名）, data（工具给界面的结构化数据，见各工具）, status: done|error|denied|interrupted|skipped|lost, duration}`。助手消息的 `meta`：`{model, usage, stop, step, partial?}`；出错的运行会追加一条 `parts[0].status=error, data.error` 的助手消息。

### 24.4 智能体事件

WebSocket 消息 `{"t":"run_event","d":{"run_id","conversation_id","seq","type","data","at"}}`，SSE 里是 `{"seq","type","data","at"}`。同一运行的 `seq` 从 1 递增；断线后用 `GET /agent/runs/{id}` 的 `resume_from`（当前这一步开头之前的序号）续传，`pending` 是还在等待回应的交互。

| type | data |
|---|---|
| `run_start` | `{run_id, conversation_id}` |
| `user_message` | 消息对象（含插话） |
| `steer_queued` / `queued` | `{text}`：插话已收下 / 已排队 |
| `step_start` | `{step, last}`（`last` 为步数上限的最后一步） |
| `assistant_start` / `assistant_done` | `{id, partial?}` 一次模型响应的开始与结束 |
| `text` / `reasoning` | `{id, text}` 增量 |
| `tool_input_start` / `tool_input_delta` | `{call_id, name, title}` / `{call_id, text}` 工具参数流式生成 |
| `tool_call` | `{call_id, name, title, args}` 参数完整（只读工具此时已开始执行） |
| `tool_running` | `{call_id, name}` |
| `tool_result` | `{call_id, name, status, text（≤4000 字）, data, duration, truncated_result_id}` |
| `message_saved` | `{id, seq, role, synthetic?}` |
| `step_reset` | `{reason}`：这一步没有输出就失败，丢弃已显示的半截内容（随后重试） |
| `retry` | `{attempt, wait_ms, kind, label, error}` |
| `usage` | `{context, limit, window, total:{input, output, cache_read, cache_write, reasoning, requests}}` |
| `approval` | 审批卡片（§24.2 字段）+ `{id, kind:"approval"}` |
| `question` | `{id, kind:"question", questions:[{question, header, options:[{label, description}], multiple}]}`；没有 options 是填空题 |
| `doom_loop` | `{id, kind:"doom_loop", call_id, tool, args, message}` 同一工具同样参数连续 3 次 |
| `interaction_done` | `{id, reply}` |
| `todos` | `{todos:[{content, status, priority}]}` |
| `compaction_start` / `compaction_done` | `{reason: auto\|overflow}` / `{ok, summary?, upto?}` |
| `system_update` | `{text}` |
| `subagent_start` / `subagent_done` | `{task_id, description, type, resumed}` / `{task_id, ok, error?}` |
| `subagent_event` | `{task_id, type: tool_call\|tool_result\|retry\|notice, data}` |
| `notice` | `{level: info\|warn, text}` |
| `title` | `{title}` |
| `next_run` | `{run_id}` 排队的消息开始了新一轮 |
| `run_done` | `{status: done\|error\|stopped, error}` |

交互回应：`approval` 与 `doom_loop` 用 `reply = once | always | reject`（`always` 对审批按「工具 + 连接 + 语句类别」记在会话里，并自动放行其他已满足的挂起请求；`reject` 带 `message` 时作为工具错误回灌、循环继续，不带说明时结束本步）。`question` 用 `answers`：每题一个字符串或字符串数组，可附 `message`。

### 24.5 agent loop 机制（§16.2 的实现位置）

| 机制 | 实现 |
|---|---|
| 工具边流边执行，只读并发 ≤ 4、写入与提问与浏览器工具按顺序 | `agent/step.go` executor |
| 坏的工具调用：名字大小写/别名修正；参数按 schema 修复（深度 ≤ 6），修不好列出问题 + 原参数回灌 | `tools.Resolve`、`tools.Repair`、`tools.Registry.Call` |
| 输出 > 2000 行或 50 KB 截断，完整内容存结果，提示 `read_result` 或子智能体 | `tools.Env.truncate` |
| finish=length 续写最多两次；最后一步只总结 + `tool_choice=none`（保留工具定义） | `agent/loop.go` |
| 错误分类与重试（2 秒起指数退避、封顶 10 秒、抖动、最多 10 次；retry-after 封顶 15 分钟；x-should-retry；网络不可达单独 1 小时预算）；上下文溢出触发压缩（0.85/0.7/0.5/0.35 逐级）；有输出后断流保存并开新一步 | `llm/errors.go`、`agent/retry.go`、`agent/step.go`、`agent/loop.go` |
| 死循环检测 | `agent/step.go` doomCheck |
| 审批三种答复 | `agent/run.go` respond、`agent/loop.go` approver |
| 插话 / 排队 | `agent/service.go` Send、`agent/loop.go` flushSteer |
| 停止、强制中断、重启恢复（残留运行与压缩标失败，没执行完的工具补「丢失」结果，回放丢掉没执行的调用与尾部空思考） | `agent/step.go` wait、`agent/service.go` Recover、`agent/context.go` toLLM |
| 压缩：上限 = 窗口 − max(15%, 16k)，估算 = 上次实测 + 新增估算；保留最近约 15k token；固定模板，不合格补发一次 | `agent/compact.go` |
| 系统提示固定 + `<system-update>`；Anthropic 缓存断点（系统、工具、最后一条、上一步前缀） | `agent/context.go`、`llm/anthropic.go` |
| 子智能体 explore / analyst，可并发，`task_id` 续接 | `agent/sub.go` |
| 计划清单、计划模式、标题、用量 | `tools/agenttools.go`、`agent/loop.go`、`agent/service.go` |

新的 Claude 模型（Anthropic Messages 协议）思考参数用 `thinking:{type:"adaptive"}` + `output_config.effort`，较早的模型与兼容实现用 `budget_tokens`；按模型名先猜，供应商报参数不对时换另一种重试一次并记住。

### 24.6 浏览器端工具（`client_call`）

服务端推 `{"t":"client_call","d":{"id","tool","args"}}`，浏览器在主工作区执行后回 `{"t":"client_result","d":{"id","result"| "error"}}`（2 分钟超时）。只有用户有在线的浏览器连接时这些工具才出现在工具列表里；发消息时带 `X-Lemi-Conn` 头的那个连接优先。权限与审批在服务端先做完；`args.visible_connections` 是本次对智能体可见的连接 ID，浏览器不得操作其他连接的标签。浏览器回传的数据服务端会再按权限过滤。

| 工具 | args | result |
|---|---|---|
| `workspace_open_table` | `{connection_id, db, table, where?, order_by?, columns?:[列名], visible_connections}` | `{tab_id, rows_loaded, total?, error?}` |
| `workspace_run_sql` | `{connection_id, db?, sql, approved（写语句已审批）, visible_connections}` | `{tab_id, results:[{kind:"rows"\|"exec"\|"error", rows_loaded?, affected?, error?, duration_ms}]}` |
| `workspace_update_view` | `{tab_id?（默认当前标签）, where?, order_by?, columns?, goto_row?, visible_connections}` | `{tab_id, connection_id, rows_loaded, error?}` |
| `workspace_read_view` | `{tab_id?, max_rows（≤100）, visible_connections}` | `{tab_id, kind, connection_id, db, table, where, order_by, sql, selection:{rows:[行号], columns:[列名], cells?:[[行号,列名,值]]}, visible_rows_sample:{columns:[…], rows:[[…]]}, error?}` |

`where` / `order_by` 是不含关键字的 SQL 片段，服务端已检查不能有多条语句；表格页按 §9 的只读查询执行。

### 24.7 MCP 与 OAuth

- `/mcp`：Streamable HTTP。请求头 `Mcp-Protocol-Version` ≥ `2026-07-28` 的走无会话处理器，其余（`2025-11-25`、`2025-06-18`、`2025-03-26`、`2024-11-05`）走有会话处理器（`Mcp-Session-Id`，空闲 30 分钟关闭）。两者共用同一个 MCP Server，工具处理函数每次按当前请求头里的令牌重新确认身份与授权，令牌吊销、账号停用立刻生效。
- 鉴权失败 401，`WWW-Authenticate: Bearer resource_metadata="<对外地址>/.well-known/oauth-protected-resource"`。每个授权每分钟 120 次请求，超出 429 + `Retry-After`。
- 防 DNS 重绑定：配置了 `LEMI_PUBLIC_URL` 时 Host 必须是它（或回环地址、IP）；没配置时，经回环地址直连（非受信代理转发）的请求 Host 也必须是回环地址或 IP。带 `Origin` 的请求必须与本站同源。
- 工具：§16.3 中除 `workspace_*`、`todo_write`、`task`、`ask_user` 之外的全部；注解 `readOnlyHint`（只读工具）、`destructiveHint`（`execute_sql`）。`create_chart` 在 MCP 里额外返回最终 ECharts option JSON。写入的审批交给外部智能体（`execute_sql` 标了 destructive）。
- 授权档位只能是 `hidden / read / write_auto / full`，且不高于用户在该连接上的实际档位（连接是「逐条确认」时最高只读）。

公开端点（不走会话与 CSRF，允许跨域）：

| 路径 | 说明 |
|---|---|
| `GET /.well-known/oauth-protected-resource[/mcp]` | 资源元数据（RFC 9728） |
| `GET /.well-known/oauth-authorization-server[/mcp]`、`/.well-known/openid-configuration` | 授权服务器元数据（RFC 8414） |
| `POST /oauth/register` | 动态注册公开客户端（RFC 7591），回调只允许 https、本机回环 http、自定义协议；每 IP 每小时 20 次 |
| `GET /oauth/authorize` | 302 到前端确认页 `/connect/authorize?<原查询参数>` |
| `POST /oauth/token` | `authorization_code`（PKCE S256 必须）与 `refresh_token`（每次轮换，旧的再用整条链作废；授权码重放作废已发令牌）；访问令牌 1 小时（`lemi_oat_`），刷新令牌 60 天（`lemi_ort_`），都不超过授权有效期 |
| `POST /oauth/revoke` | RFC 7009，总是 200 |

需要登录的接口：

| 方法与路径 | 说明 |
|---|---|
| `POST /oauth/authorize-info` | 确认页加载：body 为原查询参数 `{response_type, client_id, redirect_uri, state, scope, code_challenge, code_challenge_method, resource}` → `{ok, error, error_description, client_id, client_name, redirect_host, levels, workspaces:[{id, name, role, connections:[{id, name, driver, env, ai_level, max_level}]}]}` |
| `POST /oauth/consent` | 用户确认：上面的参数 + `{approve, workspace_id, levels:{连接ID: 档位}, expires_days（0 = 不过期）}` → `{redirect}`（前端跳过去；拒绝或参数错误时 redirect 带 `error`） |
| `GET /me/mcp/grants` | 已授权应用 `{grants:[{id, kind, name, client_id, workspace_id, workspace_name, levels, expires_at, created_at, last_used_at, expired}]}` |
| `DELETE /me/mcp/grants/{id}` | 吊销（全部令牌立刻失效） |
| `GET /me/mcp/tokens` | 个人访问令牌 `{tokens:[…同上, token_hint]}` |
| `POST /me/mcp/tokens` | 创建（需要 10 分钟内验证过身份 re-auth）`{name, workspace_id, levels, expires_days}` → `{token（lemi_pat_…，只显示这一次）, grant}` |
| `DELETE /me/mcp/tokens/{id}` | 吊销 |
| `GET /me/mcp/choices?workspace=` | 当前用户在该空间可授权的连接与档位上限 `{connections:[{id, name, driver, env, ai_level, max_level}], levels}`（建令牌、定时任务选档位用；查看者及以上） |
| `GET /me/mcp/config` | 客户端配置片段 `{url, clients:[{id: claude-code\|cursor\|codex\|claude-desktop, name, format, file?, oauth, token}]}` |

### 24.8 工具结果的 `data`（给界面）

| 工具 | data |
|---|---|
| `list_connections` | `{connections:[{id, name, driver, env, ai_level, read_only, allow}]}` |
| `run_query` | `{result_id, connection_id, db, sql, title, columns, rows（前 ≤100 行）, row_count, truncated, duration_ms}` |
| `read_result` | `{result_id, columns, rows, total}` |
| `explain_query` | `{format:"json", plan}` 或 `{format:"table", columns, rows}` |
| `execute_sql` | `{connection_id, statements:[{statement, kind, affected, error, duration_ms, class}]}` |
| `create_chart` | `{chart_id, title, rows}`；`create_diagram` / `er_diagram`：`{diagram_id, title}` |
| `report_write` | `{report_id, title, created, warnings}` |
| `todo_write` | `{todos}` |

聊天正文里的 ` ```lemi-chart <id>``` `、` ```lemi-diagram <id>``` `、` ```lemi-table <结果ID>``` ` 由前端用 §24.3 的图表与结果接口渲染。

### 24.9 报告

| 方法与路径 | 说明 |
|---|---|
| `GET /workspaces/{ws}/reports` | 列表（查看者）`{reports:[{id, title, created_by, updated_by, conversation_id, task_id, created_at, updated_at, shared}]}` |
| `POST /workspaces/{ws}/reports` | 新建（成员）`{title, body}` → `{report, warnings}` |
| `GET /reports/{id}` | `{report:{id, workspace_id, title, body, snapshot:{charts:{id: 图表视图}, tables:{结果ID: {title, sql, columns, rows（≤1000）, total, truncated}}, diagrams:{id: 图视图}, missing}, share:{enabled, has_password, expires_at, created_at}\|null, …}}` |
| `PUT /reports/{id}` | `{title?, body?}`，保存时重做快照（引用不到的沿用旧快照） |
| `DELETE /reports/{id}` | 创建者或空间管理员 |
| `GET /reports/{id}/export.md` | Markdown 导出：表格内嵌为 Markdown 表格，图表附数据表，图为 mermaid 代码块 |
| `POST /reports/{id}/share` | 生成公开链接（创建者或空间管理员；受 `AllowPublicShare`）`{password?, expires_days?}` → `{token, path:"/share/reports/<token>", url, api:"/api/public/reports/<token>", expires_at, has_password}`；再次调用换新令牌 |
| `DELETE /reports/{id}/share` | 撤销 |
| `GET /api/public/reports/{token}`（公开） | 只读 JSON `{report:{title, body, snapshot, updated_at, expires_at}}`；有密码时放在请求头 `X-Lemi-Report-Password`，缺少回 401 `password_required`、错误回 401 `bad_password`；每 IP 每分钟 30 次，密码错误每分钟 5 次 |

引用块：单独成行的 ` ```lemi-chart <id>``` `，或多行（开头一行 ` ```lemi-table <id> `，下面到 ` ``` ` 为说明文字）。`lemi-table` 只能引用自己的结果（结果保留 7 天，快照后不再依赖）。

### 24.10 定时智能体任务

| 方法与路径 | 说明 |
|---|---|
| `GET /workspaces/{ws}/agent-tasks` | 列表（成员）`{tasks:[任务视图 + running]}` |
| `POST /workspaces/{ws}/agent-tasks` | 新建（空间管理员）`Input` → `{task}` |
| `GET/PUT/DELETE /agent-tasks/{id}` | 查看（成员）/ 修改 / 删除（空间管理员） |
| `POST /agent-tasks/{id}/run` | 立即运行 → `{run_id}`（同一任务同时只跑一个，全局最多 4 个） |
| `POST /agent-tasks/{id}/stop` | 停止 → `{stopped}` |
| `GET /agent-tasks/{id}/runs?limit=` | 运行记录 `{runs:[{id, conversation_id, trigger, status, summary, report_id, error, started_at, finished_at}]}` |
| `GET /agent-tasks/{id}/runs/{run}/transcript` | 完整过程 `{conversation, messages, todos, run}` |

`Input`：`{name, schedule（§sched.Spec：kind hourly|daily|weekly|monthly|cron|manual …）, prompt, model, effort, connections:{连接ID: read|write_auto|full}（空值 = 只读；不高于运行身份在该连接上的实际档位）, channels:[通知渠道ID], notify_on: always|failure|never, enabled, owner_id（以谁的身份运行，默认创建者）}`。任务视图另有 `schedule_text, next_run_at, last_run_at, last_status`。

每次运行是一段独立对话（`kind=task`），无人值守：需要确认的写入一律拒绝、没有 `ask_user` 与浏览器工具；最后一条助手文字作为摘要，最后写的报告记为 `report_id`，按 `notify_on` 经 `TaskNotifier` 发出（链接 `<对外地址>/agent-tasks/<任务ID>?run=<运行ID>`）。

### 24.11 模型供应商（实例管理员）

| 方法与路径 | 说明 |
|---|---|
| `GET /admin/ai` | `{providers:[{id, kind, name, base_url, protocol, models, auto_models, enabled, api_key:{set}}], default_model, protocols}` |
| `POST /admin/ai/providers` | 新建 `{kind:"opencode-go"\|"custom", name, base_url, api_key, protocol, models:[{id, name, context, max_output, vision, efforts}], auto_models, enabled}` |
| `PUT /admin/ai/providers/{id}` | 修改（`api_key` 为空表示不变；自定义地址换了主机必须重填密钥） |
| `DELETE /admin/ai/providers/{id}` | 删除 |
| `GET /admin/ai/providers/{id}/models?fetch=1` | 当前模型列表；`fetch=1` 时自定义供应商从 `<base_url>/models` 现拉 |
| `POST /admin/ai/providers/test` | 不保存先测试：同新建参数 + `provider_id?`（沿用已存密钥）+ `model?`（发一句话）→ `{ok, models, reply?, error?}` |
| `PUT /admin/ai/settings` | `{default_model}` |

- 密钥用主密钥加密（AAD = `ai_providers` + ID + `api_key`），接口只返回 `{set}`。
- OpenCode Go：模型列表 `GET https://opencode.ai/zen/go/v1/models`（`base_url` 可覆盖），每个模型按推荐协议与它自己的 base_url 调用；请求带 `x-opencode-session`（会话 ID 的 ASCII 部分）与 `User-Agent: lemi/<版本>`。首次启动若设置了 `LEMI_OPENCODE_API_KEY` 且还没有任何供应商，自动写入；`LEMI_LLM_DEFAULT_MODEL` 写入实例默认模型。
- 自定义供应商的地址经 netsafe：内网地址需要实例管理员打开 `AllowPrivateEgress`，建连时按实际 IP 检查（防 DNS 重绑定）。
- 模型引用 `ref` = `<供应商 ID>/<模型 ID>`。默认模型的选择顺序：空间默认 → 实例默认 → 推荐模型里套餐额度最大的。
- `/api/v1/bootstrap` 附加 `ai:{configured}`。
