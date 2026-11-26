// 报告：Markdown + 图表/表格/图引用块（保存时做数据快照）；空间内共享、公开链接、导出 HTML / PDF / Markdown。
import { defineModule } from '@/modules/registry'
import { useTabs } from '@/stores/tabs'

/** 打开报告标签（同一报告只开一个）。 */
export function openReport(id: string, title = '') {
  return useTabs().open('report', { reportId: id }, { title: title || id, key: 'report:' + id })
}

export default defineModule({
  name: 'reports',
  tabs: [{ type: 'report', icon: 'report', component: () => import('./ReportTab.vue') }],
  rail: [{ id: 'reports', icon: 'report', label: 'reports.title', order: 20, panel: () => import('./ReportsPanel.vue'), mobile: true }],
  topRoutes: [{ path: '/share/reports/:token', name: 'shared-report', component: () => import('./SharedReportView.vue'), meta: { anyAuth: true } }],
})
