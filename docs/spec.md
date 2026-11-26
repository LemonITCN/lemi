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

### 3.3 命令行

```
lemi serve [--data DIR] [--listen ADDR]        启动（默认命令）
lemi admin reset-password <用户名>              重置密码并清除两步验证，打印临时密码（需要本机文件权限）
lemi admin reset-totp <用户名>
lemi admin unlock <用户名|IP>                   解除登录锁定
lemi service install|uninstall|start|stop|status [--data DIR] [--listen ADDR]
                                               装成 systemd / launchd / Windows 服务
lemi update [--check]                          检查并升级到最新正式版（校验发布签名）
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
- 版本：8.0（服务于 5.7 与 8.0 服务器）、8.4 LTS、9.x 最新。服务器版本 → 客户端版本映射写在清单里。
- 清单 `lemi-service/internal/dumptools/manifest.json`（随代码发布）：每个 版本 × 平台 的文件名与 SHA-256。Lemi 下载 `<LEMI_TOOLS_BASE_URL>/<文件名>`，校验 SHA-256 后解压到 `<数据目录>/tools/mysql-<版本>/`。校验失败删除并报错。
- 也可以在设置里指定服务器上已安装的 mysqldump 路径（Lemi 读取其版本号）。Docker 镜像内预装。

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
