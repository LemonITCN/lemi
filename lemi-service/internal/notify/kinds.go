package notify

// 渠道类型与表单描述（前端按它渲染配置表单，加渠道类型不用改前端）。字段的 Key 与配置 JSON 的键一一对应。

// 渠道类型。
const (
	KindEmail    = "email"
	KindWebhook  = "webhook"
	KindDingTalk = "dingtalk"
	KindWeCom    = "wecom"
	KindFeishu   = "feishu"
)

// Option 是下拉选项。
type Option struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Field 是渠道配置表单的一个字段。
type Field struct {
	Key         string   `json:"key"`
	Label       string   `json:"label"`
	LabelEN     string   `json:"label_en,omitempty"`
	Type        string   `json:"type"` // text | password | textarea | select | bool | number | list
	Required    bool     `json:"required,omitempty"`
	Secret      bool     `json:"secret,omitempty"` // 加密保存、只写不读；更新时不传表示保持原值
	Default     any      `json:"default,omitempty"`
	Placeholder string   `json:"placeholder,omitempty"`
	Help        string   `json:"help,omitempty"`
	Options     []Option `json:"options,omitempty"`
	// ShowIf 仅当另一个字段等于某些值时显示，例如 {"use_system": ["false"]}
	ShowIf map[string][]string `json:"show_if,omitempty"`
	// address 为真的字段是「发往哪里」：它变了，已保存的其他密钥必须重新输入（防止把旧密钥带去别的地址）
	address bool
}

// KindInfo 是一种渠道类型的描述。
type KindInfo struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	NameEN string  `json:"name_en"`
	Help   string  `json:"help,omitempty"`
	Fields []Field `json:"fields"`
}

func (k KindInfo) field(key string) (Field, bool) {
	for _, f := range k.Fields {
		if f.Key == key {
			return f, true
		}
	}
	return Field{}, false
}

func (k KindInfo) secretKeys() []string {
	var out []string
	for _, f := range k.Fields {
		if f.Secret {
			out = append(out, f.Key)
		}
	}
	return out
}

// Kinds 返回全部渠道类型（副本），按界面展示顺序。
func Kinds() []KindInfo {
	out := make([]KindInfo, len(kindList))
	for i, k := range kindList {
		k.Fields = append([]Field(nil), k.Fields...)
		out[i] = k
	}
	return out
}

func kindInfo(id string) (KindInfo, bool) {
	for _, k := range kindList {
		if k.ID == id {
			return k, true
		}
	}
	return KindInfo{}, false
}

var tlsOptions = []Option{
	{Value: "ssl", Label: "SSL/TLS（通常 465 端口）"},
	{Value: "starttls", Label: "STARTTLS（通常 587 端口）"},
	{Value: "none", Label: "不加密（只用于内网中继，不能发送口令）"},
}

var smtpOnly = map[string][]string{"use_system": {"false"}}

var kindList = []KindInfo{
	{
		ID: KindEmail, Name: "邮件", NameEN: "Email",
		Fields: []Field{
			{Key: "recipients", Label: "收件人", LabelEN: "Recipients", Type: "list", Required: true, Placeholder: "ops@example.com",
				Help: "可以填多个，每行一个或用逗号分隔，最多 50 个。"},
			{Key: "use_system", Label: "使用系统邮件服务器", LabelEN: "Use system mail server", Type: "bool", Default: true,
				Help: "系统邮件服务器由实例管理员在系统设置里配置。关闭后可以填写这个渠道专用的 SMTP 服务器。"},
			{Key: "host", Label: "SMTP 主机", LabelEN: "SMTP host", Type: "text", Placeholder: "smtp.example.com", ShowIf: smtpOnly, address: true},
			{Key: "port", Label: "端口", LabelEN: "Port", Type: "number", ShowIf: smtpOnly, address: true,
				Help: "留空时 SSL/TLS 用 465，STARTTLS 用 587，不加密用 25。"},
			{Key: "tls", Label: "加密方式", LabelEN: "Encryption", Type: "select", Default: "ssl", Options: tlsOptions, ShowIf: smtpOnly},
			{Key: "username", Label: "用户名", LabelEN: "Username", Type: "text", ShowIf: smtpOnly},
			{Key: "password", Label: "口令", LabelEN: "Password", Type: "password", Secret: true, ShowIf: smtpOnly,
				Help: "QQ 邮箱、163 邮箱等请填「授权码」，不是登录口令。主机或端口改了需要重新输入。"},
			{Key: "from", Label: "发件人", LabelEN: "From", Type: "text", Placeholder: "Lemi <noreply@example.com>", ShowIf: smtpOnly},
		},
	},
	{
		ID: KindWebhook, Name: "Webhook", NameEN: "Webhook",
		Help: "正文模板可用变量 {{title}} {{message}} {{level}} {{link}} {{event}}，按 Content-Type 自动转义。" +
			"填写签名密钥后，请求带 X-Lemi-Timestamp（Unix 秒）与 X-Lemi-Signature: sha256=<hex>，" +
			"其中 hex = HMAC-SHA256(签名密钥, 时间戳 + \".\" + 正文)；GET 请求的「正文」是完整查询串（? 之后的部分）。" +
			"接收方请用同样的算法重算并做常量时间比较，同时拒绝与当前时间相差 5 分钟以上的时间戳。",
		Fields: []Field{
			{Key: "url", Label: "地址", LabelEN: "URL", Type: "password", Required: true, Secret: true, Placeholder: "https://hooks.example.com/lemi?token=...",
				Help: "地址可能含令牌，按密钥保存，保存后不再显示。换地址时自定义请求头与签名密钥需要重新输入。"},
			{Key: "method", Label: "请求方法", LabelEN: "Method", Type: "select", Default: "POST",
				Options: []Option{{Value: "POST", Label: "POST"}, {Value: "GET", Label: "GET"}},
				Help:    "GET 不带正文，事件字段 event、level、title、message、link 作为查询参数附加到地址上。"},
			{Key: "content_type", Label: "Content-Type", LabelEN: "Content-Type", Type: "select", Default: "application/json",
				Options: []Option{{Value: "application/json", Label: "application/json"}, {Value: "application/x-www-form-urlencoded", Label: "application/x-www-form-urlencoded"}, {Value: "text/plain", Label: "text/plain"}},
				ShowIf:  map[string][]string{"method": {"POST"}}},
			{Key: "body", Label: "正文模板", LabelEN: "Body template", Type: "textarea", Placeholder: defaultWebhookBody,
				ShowIf: map[string][]string{"method": {"POST"}}, Help: "留空时按 Content-Type 使用默认模板（JSON 对象或表单字段）。"},
			{Key: "headers", Label: "自定义请求头", LabelEN: "Custom headers", Type: "textarea", Secret: true, Placeholder: "Authorization: Bearer ...",
				Help: "每行一个「名称: 值」，最多 20 个。值可能含令牌，整体按密钥保存。"},
			{Key: "sign_secret", Label: "签名密钥", LabelEN: "Signing secret", Type: "password", Secret: true,
				Help: "可选。填写后每个请求都带 HMAC-SHA256 签名，接收方可据此确认请求来自 Lemi 且没有被篡改。"},
		},
	},
	{
		ID: KindDingTalk, Name: "钉钉群机器人", NameEN: "DingTalk bot",
		Help: "在钉钉群设置 → 机器人 → 添加「自定义」机器人。安全设置选「加签」时填写签名密钥；选「自定义关键词」时可以把 Lemi 设为关键词（每条消息都含 Lemi）。",
		Fields: []Field{
			{Key: "url", Label: "Webhook 地址", LabelEN: "Webhook URL", Type: "password", Required: true, Secret: true,
				Placeholder: "https://oapi.dingtalk.com/robot/send?access_token=...", Help: "地址含访问令牌，按密钥保存，保存后不再显示。"},
			{Key: "secret", Label: "加签密钥", LabelEN: "Signing secret", Type: "password", Secret: true, Placeholder: "SEC...",
				Help: "可选。机器人安全设置里「加签」下方 SEC 开头的字符串。"},
		},
	},
	{
		ID: KindWeCom, Name: "企业微信群机器人", NameEN: "WeCom bot",
		Help: "在企业微信群聊里添加「消息推送」（群机器人），复制 Webhook 地址。每个机器人每分钟最多 20 条消息。",
		Fields: []Field{
			{Key: "url", Label: "Webhook 地址", LabelEN: "Webhook URL", Type: "password", Required: true, Secret: true,
				Placeholder: "https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=...", Help: "地址含密钥，按密钥保存，保存后不再显示。"},
		},
	},
	{
		ID: KindFeishu, Name: "飞书群机器人", NameEN: "Feishu / Lark bot",
		Help: "在飞书群设置 → 群机器人 → 添加「自定义机器人」。安全设置开启「签名校验」时填写签名密钥。",
		Fields: []Field{
			{Key: "url", Label: "Webhook 地址", LabelEN: "Webhook URL", Type: "password", Required: true, Secret: true,
				Placeholder: "https://open.feishu.cn/open-apis/bot/v2/hook/...", Help: "地址含令牌，按密钥保存，保存后不再显示。"},
			{Key: "secret", Label: "签名密钥", LabelEN: "Signing secret", Type: "password", Secret: true,
				Help: "可选。机器人安全设置里「签名校验」显示的密钥。"},
		},
	},
}

// EventInfo 是一种事件的描述（备份计划、健康检查等配置通知时展示）。
type EventInfo struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	NameEN string `json:"name_en"`
}

var eventList = []EventInfo{
	{EvBackupSuccess, "备份成功", "Backup succeeded"},
	{EvBackupFailed, "备份失败", "Backup failed"},
	{EvRestoreDone, "还原完成", "Restore completed"},
	{EvRestoreFailed, "还原失败", "Restore failed"},
	{EvVerifyFailed, "还原校验失败", "Restore verification failed"},
	{EvConnDown, "连接下线", "Connection down"},
	{EvConnUp, "连接恢复", "Connection recovered"},
	{EvTaskResult, "定时任务结果", "Scheduled task result"},
	{EvSecurity, "安全事件", "Security event"},
	{EvTest, "测试消息", "Test message"},
}

// Events 返回全部事件类型。
func Events() []EventInfo { return append([]EventInfo(nil), eventList...) }

func eventName(kind string) string {
	for _, e := range eventList {
		if e.ID == kind {
			return e.Name
		}
	}
	return kind
}

func levelName(l string) string {
	switch l {
	case LevelError:
		return "错误"
	case LevelWarning:
		return "警告"
	}
	return "信息"
}
