// 对象树右键菜单的扩展点：其他模块（控制台、表结构、备份、智能体…）按对象类型追加菜单项。
import type { MenuItem } from '@/components/ui/Menu.vue'
import type { Conn, ObjectInfo } from '@/stores/conns'

export interface TreeContext {
  conn: Conn
  database?: string
  name?: string
  object?: ObjectInfo
}

/** kind：connection | database | table | view | procedure | function | trigger | event */
type Provider = { kinds: string[]; order: number; items: (ctx: TreeContext, kind: string) => MenuItem[] }

const providers: Provider[] = []

export function registerTreeAction(kinds: string[], items: Provider['items'], order = 100) {
  providers.push({ kinds, items, order })
  providers.sort((a, b) => a.order - b.order)
}

export function treeActions(kind: string, ctx: TreeContext): MenuItem[] {
  const out: MenuItem[] = []
  for (const p of providers) {
    if (!p.kinds.includes(kind)) continue
    const items = p.items(ctx, kind)
    if (items.length) out.push({ separator: true }, ...items)
  }
  return out
}
