package target

// 各种目标的表单描述（前端按它渲染配置表单）。字段的 Key 与各后端配置结构体的 json 键一一对应。

// Kinds 返回全部目标类型，按界面展示顺序。返回的是副本，调用方可以随意修改。
func Kinds() []KindInfo {
	out := make([]KindInfo, len(kindList))
	for i, k := range kindList {
		out[i] = copyKind(k)
	}
	return out
}

// Info 返回一种目标类型的描述。
func Info(kind string) (KindInfo, bool) {
	for _, k := range kindList {
		if k.ID == kind {
			return copyKind(k), true
		}
	}
	return KindInfo{}, false
}

func copyKind(k KindInfo) KindInfo {
	k.Fields = append([]Field(nil), k.Fields...)
	for i := range k.Fields {
		k.Fields[i].Options = append([]string(nil), k.Fields[i].Options...)
	}
	return k
}

func prefixField(help string) Field {
	return Field{Key: "prefix", Label: "子目录", LabelEN: "Prefix", Type: "text", Placeholder: "lemi/backups", Help: help}
}

var kindList = []KindInfo{
	{
		ID: KindLocal, Name: "本地目录 / NAS", NameEN: "Local folder / NAS", AdminOnly: true, Network: false,
		Fields: []Field{
			{Key: "path", Label: "目录", LabelEN: "Folder", Type: "text", Placeholder: "/mnt/nas/backups",
				Help: "Lemi 所在主机上的绝对路径。留空使用数据目录下的 backups 目录。填写的目录必须已经存在（NAS 请先挂载），不会自动创建，以免 NAS 没挂载时备份悄悄写到本机磁盘。"},
		},
	},
	{
		ID: KindSFTP, Name: "SFTP", NameEN: "SFTP", Network: true,
		Fields: []Field{
			{Key: "host", Label: "主机", LabelEN: "Host", Type: "text", Required: true, Placeholder: "backup.example.com"},
			{Key: "port", Label: "端口", LabelEN: "Port", Type: "number", Default: 22},
			{Key: "username", Label: "用户名", LabelEN: "Username", Type: "text", Required: true},
			{Key: "password", Label: "口令", LabelEN: "Password", Type: "password", Secret: true,
				Help: "口令与私钥至少填一个。"},
			{Key: "private_key", Label: "私钥", LabelEN: "Private key", Type: "textarea", Secret: true,
				Placeholder: "-----BEGIN OPENSSH PRIVATE KEY-----", Help: "OpenSSH 或 PEM 格式的私钥全文。"},
			{Key: "passphrase", Label: "私钥口令", LabelEN: "Key passphrase", Type: "password", Secret: true},
			{Key: "host_key_sha256", Label: "主机密钥指纹", LabelEN: "Host key fingerprint", Type: "text", Placeholder: "SHA256:...",
				Help: "首次测试连接时会显示服务器出示的指纹，请与服务器上 ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub 的输出核对后确认。指纹变化时拒绝连接。"},
			{Key: "path", Label: "备份目录", LabelEN: "Folder", Type: "text", Placeholder: "/data/backups",
				Help: "绝对路径，或相对登录后默认目录的路径；留空为默认目录。"},
		},
	},
	{
		ID: KindWebDAV, Name: "WebDAV", NameEN: "WebDAV", Network: true,
		Fields: []Field{
			{Key: "url", Label: "地址", LabelEN: "URL", Type: "text", Required: true, Placeholder: "https://dav.jianguoyun.com/dav/"},
			{Key: "username", Label: "用户名", LabelEN: "Username", Type: "text"},
			{Key: "password", Label: "口令", LabelEN: "Password", Type: "password", Secret: true,
				Help: "坚果云等服务请填「第三方应用密码」，不是登录密码。"},
			prefixField("备份放在地址下的这个子目录里，不存在时自动创建。"),
			{Key: "spool", Label: "先暂存到本机再上传", LabelEN: "Spool locally before upload", Type: "bool", Default: false,
				Help: "默认用分块传输（chunked）边生成边上传。坚果云等不接受分块传输的服务器请勾选：备份先暂存到 Lemi 主机的临时目录，生成完再带文件大小上传，需要相应的本机磁盘空间。"},
		},
	},
	{
		ID: KindFTP, Name: "FTP / FTPS", NameEN: "FTP / FTPS", Network: true,
		Fields: []Field{
			{Key: "host", Label: "主机", LabelEN: "Host", Type: "text", Required: true, Placeholder: "ftp.example.com"},
			{Key: "port", Label: "端口", LabelEN: "Port", Type: "number", Help: "留空时明文与显式加密用 21，隐式加密用 990。"},
			{Key: "username", Label: "用户名", LabelEN: "Username", Type: "text", Required: true},
			{Key: "password", Label: "口令", LabelEN: "Password", Type: "password", Secret: true},
			{Key: "tls", Label: "加密方式", LabelEN: "Encryption", Type: "select", Default: "none",
				Options: []string{"none", "explicit", "implicit"},
				Help:    "none：不加密（口令与数据明文传输，只建议在内网使用）；explicit：显式 TLS（AUTH TLS，即 FTPES）；implicit：隐式 TLS（端口一般是 990）。使用被动模式。"},
			{Key: "insecure_skip_verify", Label: "不校验服务器证书", LabelEN: "Skip certificate verification", Type: "bool", Default: false,
				Help: "只在服务器使用自签名证书时勾选；勾选后无法防范中间人攻击。"},
			{Key: "path", Label: "备份目录", LabelEN: "Folder", Type: "text", Placeholder: "/backups",
				Help: "绝对路径，或相对登录后默认目录的路径；留空为默认目录。"},
		},
	},
	{
		ID: KindSMB, Name: "SMB 共享", NameEN: "SMB share", Network: true,
		Fields: []Field{
			{Key: "host", Label: "主机", LabelEN: "Host", Type: "text", Required: true, Placeholder: "nas.local"},
			{Key: "port", Label: "端口", LabelEN: "Port", Type: "number", Default: 445},
			{Key: "share", Label: "共享名", LabelEN: "Share", Type: "text", Required: true, Placeholder: "backup"},
			{Key: "username", Label: "用户名", LabelEN: "Username", Type: "text", Required: true},
			{Key: "password", Label: "口令", LabelEN: "Password", Type: "password", Secret: true},
			{Key: "domain", Label: "域", LabelEN: "Domain", Type: "text", Help: "工作组环境留空。"},
			{Key: "path", Label: "子目录", LabelEN: "Folder", Type: "text", Placeholder: "lemi/backups",
				Help: "共享里的子目录，留空为共享根目录，不存在时自动创建。支持 SMB 2 / 3。"},
		},
	},
	{
		ID: KindS3, Name: "S3 兼容存储", NameEN: "S3 compatible", Network: true,
		Fields: []Field{
			{Key: "endpoint", Label: "服务地址", LabelEN: "Endpoint", Type: "text", Required: true, Placeholder: "s3.amazonaws.com",
				Help: "AWS S3、Cloudflare R2、MinIO 等的服务地址，可以带 http:// 或 https://。"},
			{Key: "region", Label: "地域", LabelEN: "Region", Type: "text", Placeholder: "us-east-1", Help: "留空时自动探测；R2 填 auto。"},
			{Key: "bucket", Label: "存储桶", LabelEN: "Bucket", Type: "text", Required: true},
			prefixField("备份放在存储桶里的这个前缀下。"),
			{Key: "access_key", Label: "Access Key", LabelEN: "Access key", Type: "text", Required: true},
			{Key: "secret_key", Label: "Secret Key", LabelEN: "Secret key", Type: "password", Required: true, Secret: true},
			{Key: "use_ssl", Label: "使用 HTTPS", LabelEN: "Use HTTPS", Type: "bool", Default: true},
			{Key: "path_style", Label: "路径风格访问", LabelEN: "Path-style access", Type: "bool", Default: false,
				Help: "MinIO 等自建服务通常需要勾选。"},
		},
	},
	{
		ID: KindCOS, Name: "腾讯云 COS", NameEN: "Tencent Cloud COS", Network: true,
		Fields: []Field{
			{Key: "region", Label: "地域", LabelEN: "Region", Type: "text", Required: true, Placeholder: "ap-guangzhou"},
			{Key: "bucket", Label: "存储桶", LabelEN: "Bucket", Type: "text", Required: true, Placeholder: "backup-1250000000",
				Help: "「名称-APPID」形式。"},
			prefixField("备份放在存储桶里的这个前缀下。"),
			{Key: "secret_id", Label: "SecretId", LabelEN: "SecretId", Type: "text", Required: true},
			{Key: "secret_key", Label: "SecretKey", LabelEN: "SecretKey", Type: "password", Required: true, Secret: true},
		},
	},
	{
		ID: KindOSS, Name: "阿里云 OSS", NameEN: "Alibaba Cloud OSS", Network: true,
		Fields: []Field{
			{Key: "region", Label: "地域", LabelEN: "Region", Type: "text", Required: true, Placeholder: "cn-hangzhou"},
			{Key: "bucket", Label: "存储桶", LabelEN: "Bucket", Type: "text", Required: true},
			prefixField("备份放在存储桶里的这个前缀下。"),
			{Key: "access_key_id", Label: "AccessKey ID", LabelEN: "AccessKey ID", Type: "text", Required: true},
			{Key: "access_key_secret", Label: "AccessKey Secret", LabelEN: "AccessKey secret", Type: "password", Required: true, Secret: true},
			{Key: "internal", Label: "走内网端点", LabelEN: "Use internal endpoint", Type: "bool", Default: false,
				Help: "Lemi 部署在同地域的阿里云 ECS 上时勾选，流量不计公网费用。"},
			{Key: "endpoint", Label: "自定义端点", LabelEN: "Custom endpoint", Type: "text", Placeholder: "backup.example.com",
				Help: "可选。2025-03-20 之后开通 OSS 的账号，在中国内地地域不能用默认公网域名读写数据，请给存储桶绑定自定义域名并填在这里；也可以填 oss-accelerate.aliyuncs.com 使用传输加速。"},
		},
	},
	{
		ID: KindAzure, Name: "Azure Blob 存储", NameEN: "Azure Blob Storage", Network: true,
		Fields: []Field{
			{Key: "account", Label: "存储账户", LabelEN: "Storage account", Type: "text", Required: true},
			{Key: "key", Label: "访问密钥", LabelEN: "Access key", Type: "password", Secret: true,
				Help: "Azure 门户「存储账户 → 访问密钥」里的密钥。填了 SAS 令牌时可以不填。"},
			{Key: "sas", Label: "SAS 令牌", LabelEN: "SAS token", Type: "password", Secret: true, Placeholder: "sv=...&sig=...",
				Help: "可选。需要读、写、删除、列出权限；填了就用它认证，不用访问密钥。"},
			{Key: "container", Label: "容器", LabelEN: "Container", Type: "text", Required: true, Help: "必须已经存在。"},
			prefixField("备份放在容器里的这个前缀下。"),
			{Key: "endpoint", Label: "服务地址", LabelEN: "Endpoint", Type: "text", Placeholder: "https://<账户>.blob.core.windows.net",
				Help: "可选。中国区填 https://<账户>.blob.core.chinacloudapi.cn；Azurite 填 http://127.0.0.1:10000/devstoreaccount1。"},
		},
	},
	{
		ID: KindBaidu, Name: "百度网盘", NameEN: "Baidu Netdisk", Network: true, OAuth: true,
		Fields: []Field{
			{Key: "app_key", Label: "AppKey", LabelEN: "AppKey", Type: "text", Required: true,
				Help: "在百度网盘开放平台创建应用后获得。"},
			{Key: "secret_key", Label: "SecretKey", LabelEN: "SecretKey", Type: "password", Required: true, Secret: true},
			{Key: "app_name", Label: "应用名称", LabelEN: "App name", Type: "text", Required: true,
				Help: "开放平台应用的名称，备份放在网盘「我的应用数据 / 应用名称」下。"},
			prefixField("应用目录下的子目录。"),
			{Key: "access_token", Label: "访问令牌", LabelEN: "Access token", Type: "password", Secret: true,
				Help: "扫码授权后自动填写，无需手动输入。"},
			{Key: "refresh_token", Label: "刷新令牌", LabelEN: "Refresh token", Type: "password", Secret: true,
				Help: "扫码授权后自动填写，无需手动输入。"},
		},
	},
}
