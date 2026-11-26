// 通知：空间通知渠道（邮件、Webhook、钉钉、企业微信、飞书，事件订阅、测试发送、发送记录）与实例的系统邮件服务器。
import { defineModule } from '@/modules/registry'

export default defineModule({
  name: 'notify',
  settings: [
    { id: 'notify-channels', label: 'notify.channels', icon: 'bell', group: 'workspace', order: 40, component: () => import('./ChannelsPage.vue'), minRole: 'viewer' },
    { id: 'smtp', label: 'notify.smtp', icon: 'mail', group: 'admin', order: 30, component: () => import('./SmtpPage.vue') },
  ],
})
