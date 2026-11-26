import { defineModule } from '@/modules/registry'
import { registerTreeAction } from '@/modules/explorer/actions'
import { openers } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { i18n } from '@/i18n'
import { quoteIdent } from '@/lib/sql'
import { dialectOf } from '@/lib/dialect'

registerTreeAction(['table', 'view'], (ctx) => [
  { label: i18n.global.t('console.selectInConsole'), icon: 'terminal', run: () => {
    const d = dialectOf(ctx.conn)
    const t = useTabs().open('console', { connectionId: ctx.conn.id, database: ctx.database, sql: `SELECT * FROM ${quoteIdent(ctx.name!, d)} LIMIT 100;`, autorun: true },
      { title: `${ctx.conn.name} · ${ctx.database}`, connectionId: ctx.conn.id })
    void t
  } },
], 10)
void openers

export default defineModule({
  name: 'console',
  tabs: [{ type: 'console', icon: 'terminal', component: () => import('./ConsoleView.vue') }],
})
