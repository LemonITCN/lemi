// 模块注册：每个功能模块在 src/modules/<名>/index.ts 默认导出 defineModule({...})，主界面自动收集。
// 增加功能（备份、智能体、报告……）只新增模块目录，不需要改动外壳。
import type { Component } from 'vue'
import type { RouteRecordRaw } from 'vue-router'

/** 主工作区里的一个标签页。 */
export interface Tab {
  id: string
  type: string
  title: string
  params: Record<string, any>
  /** 去重键：同一个键只开一个标签（例如同一张表） */
  key?: string
  dirty?: boolean
  /** 由 AI 打开或修改过 */
  fromAI?: boolean
  connectionId?: string
}

/** 标签页类型。 */
export interface TabType {
  type: string
  component: () => Promise<{ default: Component }>
  icon: string | ((tab: Tab) => string)
}

/** 左侧活动栏的一项（点开后在侧栏显示面板，或直接打开页面）。 */
export interface RailItem {
  id: string
  icon: string
  /** i18n 键 */
  label: string
  order: number
  /** 侧栏面板 */
  panel?: () => Promise<{ default: Component }>
  /** 不显示侧栏，直接跳转路由 */
  route?: string
  /** 手机底部导航里是否显示 */
  mobile?: boolean
}

/** 设置中心的一页。 */
export interface SettingsPage {
  id: string
  /** i18n 键 */
  label: string
  icon: string
  group: 'account' | 'workspace' | 'admin'
  order: number
  component: () => Promise<{ default: Component }>
  /** 空间页需要的最低角色 */
  minRole?: 'viewer' | 'member' | 'admin' | 'owner'
}

/** 智能体调用的浏览器端工具。 */
export type ClientTool = (args: any) => Promise<any>

export interface LemiModule {
  name: string
  /** 挂在主界面（已登录）下的子路由 */
  routes?: RouteRecordRaw[]
  /** 不需要主界面外壳的顶层路由（例如 OAuth 授权确认页、公开报告页） */
  topRoutes?: RouteRecordRaw[]
  tabs?: TabType[]
  rail?: RailItem[]
  settings?: SettingsPage[]
  clientTools?: Record<string, ClientTool>
  /** 右侧面板（智能体对话），只取第一个提供者 */
  rightPanel?: { icon: string; label: string; component: () => Promise<{ default: Component }> }
  /** 登录完成、进入主界面后调用一次 */
  setup?: () => void
}

export function defineModule(m: LemiModule): LemiModule { return m }

const found = import.meta.glob<{ default: LemiModule }>('./*/index.ts', { eager: true })
export const modules: LemiModule[] = Object.values(found).map((m) => m.default).filter(Boolean)

export function tabType(type: string): TabType | undefined {
  for (const m of modules) for (const t of m.tabs || []) if (t.type === type) return t
  return undefined
}

export function railItems(): RailItem[] {
  return modules.flatMap((m) => m.rail || []).sort((a, b) => a.order - b.order)
}

export function settingsPages(): SettingsPage[] {
  return modules.flatMap((m) => m.settings || []).sort((a, b) => a.order - b.order)
}

export function clientTool(name: string): ClientTool | undefined {
  for (const m of modules) if (m.clientTools?.[name]) return m.clientTools[name]
  return undefined
}

export function rightPanel() {
  for (const m of modules) if (m.rightPanel) return m.rightPanel
  return undefined
}
