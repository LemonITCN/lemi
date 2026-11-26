export default {
  login: '登录', logout: '退出登录', username: '用户名', password: '口令', passwordNew: '新口令', passwordCurrent: '当前口令', passwordConfirm: '再次输入新口令',
  passwordMismatch: '两次输入的口令不一致', passwordHint: '至少 10 位，包含字母、数字、符号中的两类',
  mfaTitle: '两步验证', mfaHint: '输入验证器 App 里的 6 位动态码，或一个恢复码', code: '验证码', verify: '验证',
  setupTitle: '初始化 Lemi', setupHint: '创建第一个管理员账号。初始化令牌在服务启动时的控制台输出里，也保存在数据目录的 setup-token 文件中。',
  setupToken: '初始化令牌', displayName: '显示名', email: '邮箱', workspaceName: '第一个工作空间', createAdmin: '创建管理员',
  totpTitle: '绑定两步验证', totpRequired: '为了账号安全，必须先绑定两步验证。用验证器 App（Google Authenticator、Microsoft Authenticator、1Password 等）扫描二维码，然后输入 6 位动态码。',
  totpManual: '无法扫码时手动输入密钥', totpBind: '绑定', recoveryTitle: '保存恢复码',
  recoveryHint: '每个恢复码只能用一次，手机丢失时用它登录。只显示这一次，请保存在安全的地方。', recoverySaved: '我已保存，继续',
  changePasswordTitle: '修改口令', mustChange: '管理员为你设置了临时口令，请先修改。',
  inviteTitle: '接受邀请', inviteHint: '创建你的账号', inviteInvalid: '邀请链接无效或已过期', register: '创建账号',
  insecure: '当前通过未加密的 HTTP 访问公网地址：口令与数据库内容都会明文传输。请启用 HTTPS。',
  reauthTitle: '验证身份', reauthHint: '这是敏感操作，请再次输入口令或两步验证码。', useCode: '改用验证码', usePassword: '改用口令',
}
