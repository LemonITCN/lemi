export default {
  newTitle: '新建连接', editTitle: '编辑连接', chooseType: '选择数据库类型', moreSoon: 'PostgreSQL、MongoDB、Elasticsearch 等将以驱动插件的方式陆续加入。',
  tabGeneral: '常规', tabSSH: 'SSH 隧道', tabProxy: '代理', test: '测试连接', port: '端口', env: '环境', envHelp: '标为「生产」的连接在界面上有红色标记，写入操作会额外提醒。',
  folder: '分组', color: '颜色', secretKept: '已设置，留空则保持不变',
  sshEnable: '通过 SSH 隧道连接', sshHelp: '先连到跳板机，再由跳板机连接数据库。首次连接需要确认跳板机的主机指纹。', sshHost: 'SSH 主机', sshAuth: '认证方式', sshKey: '私钥',
  sshPassphrase: '私钥口令', sshHostKey: '已信任的主机指纹', sshHostKeyHelp: '指纹固定后，如果跳板机的指纹变了（可能被中间人攻击或主机重装），连接会被拒绝。', sshHostKeyNone: '尚未确认（测试连接时确认）',
  hostKeyFirst: '首次连接这台 SSH 主机。请向服务器管理员核对下面的指纹，一致后再信任。', hostKeyMismatch: 'SSH 主机指纹与之前信任的不一致！可能遭到中间人攻击，或主机重装过。确认无误后才能重新信任。', trustHostKey: '信任此指纹',
  proxyType: '代理类型', proxyHelp: '经 SOCKS5 或 HTTP CONNECT 代理连接数据库（与 SSH 隧道同时使用时，先经代理连到 SSH 主机）。', proxyNone: '不使用代理', proxyHost: '代理主机',
  aiIntro: 'AI 智能体访问这个连接时的权限。只读在数据库层强制，不靠猜 SQL。实际权限还会受到使用者本人的权限限制。',
  ai: {
    hidden: '不可见', hiddenDesc: 'AI 看不到这个连接', read: '只读', readDesc: '只能查结构与数据',
    writeConfirm: '读写 · 逐条确认', writeConfirmDesc: '每一条写入都要你在界面上确认', writeAuto: '读写 · 增删改自动', writeAutoDesc: '增删改直接执行，建表、改表、删表仍要确认',
    full: '完全放开', fullDesc: '所有语句直接执行（需要实例管理员允许）',
  },
  aiAllow: '库表白名单（可选）', aiAllowHelp: '每行一个「库.表」，可用 * 通配，例如 shop.order_*。留空表示不限。白名单外的对象 AI 看不到也查不到。',
  readOnly: '只读连接', readOnlyHelp: '所有人（包括管理员）在这个连接上都只能读，在数据库会话层强制。', notes: '备注',
  sqliteUploaded: '上传的文件', sqliteServer: '服务器目录', sqliteUpload: '上传 SQLite 文件', sqliteNoUploads: '还没有上传的文件',
  sqliteNoDirs: '实例管理员没有配置允许打开的服务器目录（启动参数 LEMI_SQLITE_DIRS）。',
}
