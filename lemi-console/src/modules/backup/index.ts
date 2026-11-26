// 备份：计划、运行记录（实时进度、下载、校验、还原）、存储目标、还原记录；实例管理员的 mysqldump 工具页；
// 对象树连接右键「备份计划」「立即备份」。
import { getCurrentInstance } from 'vue'
import type { Router } from 'vue-router'
import { defineModule } from '@/modules/registry'
import { registerTreeAction } from '@/modules/explorer/actions'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'
import { watchProgress } from './api'

let router: Router | null = null

function go(path: string, query: Record<string, string>) {
  router?.push({ path, query })
}

/** 立即备份某连接：只有一个计划时直接运行，多个时打开该连接的计划列表，没有计划时打开新建。 */
async function backupNow(connId: string) {
  const session = useSession()
  const toast = useToast()
  const t = i18n.global.t
  try {
    const r = await api.get(`/workspaces/${session.current?.id}/backups/plans?connection=${connId}`)
    const plans = r.plans || []
    if (plans.length === 1) {
      await api.post(`/workspaces/${session.current?.id}/backups/plans/${plans[0].id}/run`)
      toast.success(t('backup.started'))
      go('/backups/runs', { connection: connId })
    } else if (plans.length > 1) {
      go('/backups/plans', { connection: connId })
    } else if (session.atLeast('admin')) {
      go('/backups/plans', { connection: connId, new: '1' })
    } else {
      toast.info(t('backup.noPlanForConn'))
    }
  } catch (e) { toast.error(errorText(e)) }
}

registerTreeAction(['connection'], (ctx) => {
  const { conn } = ctx
  if (conn.driver !== 'mysql' && conn.driver !== 'sqlite') return []
  const t = i18n.global.t
  const items = [{ label: t('backup.tree.plans'), icon: 'archive', run: () => go('/backups/plans', { connection: conn.id }) }]
  if (useSession().atLeast('member') && (conn.access === 'write' || conn.access === 'manage') && !conn.read_only) {
    items.push({ label: t('backup.tree.runNow'), icon: 'play', run: () => backupNow(conn.id) })
  }
  return items
}, 60)

export default defineModule({
  name: 'backup',
  routes: [{ path: 'backups/:section?', name: 'backups', component: () => import('./BackupView.vue') }],
  rail: [{ id: 'backups', icon: 'archive', label: 'backup.title', order: 40, route: '/backups', mobile: true }],
  settings: [
    { id: 'dumptools', label: 'backup.tools.title', icon: 'download', group: 'admin', order: 40, component: () => import('./ToolsPage.vue') },
  ],
  setup: () => {
    router = (getCurrentInstance()?.appContext.config.globalProperties as any)?.$router || null
    watchProgress()
  },
})
