<script setup lang="ts">
// DDL 标签：取对象的创建语句，全屏只读高亮显示；可复制、在控制台打开。
import { computed, ref, watch } from 'vue'
import type { Tab } from '@/modules/registry'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import SqlView from '@/components/code/SqlView.vue'
import { api } from '@/api/client'
import { useConns, openers } from '@/stores/conns'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { dialectOf } from '@/lib/dialect'
import { copyText } from '@/lib/cm'
import { errorText, i18n } from '@/i18n'

const props = defineProps<{ tab: Tab; active: boolean }>()
const conns = useConns()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t

const conn = computed(() => conns.byId[props.tab.params.connectionId])
const connMissing = computed(() => !conn.value && !conns.loading && conns.list.length > 0)
const db = computed(() => String(props.tab.params.database || ''))
const kind = computed(() => String(props.tab.params.kind || 'table'))
const name = computed(() => String(props.tab.params.name || ''))
const dialect = computed(() => dialectOf(conn.value))

// 对象树双击例程等直接打开时标题只有对象名：补上「· DDL」，和数据标签区分开
if (name.value && props.tab.title === name.value) useTabs().update(props.tab.id, { title: `${name.value} · DDL` })

const ddl = ref('')
const loading = ref(false)
const error = ref('')
let loaded = false

async function load() {
  if (!conn.value) return
  loading.value = true
  error.value = ''
  try {
    const q = new URLSearchParams({ db: db.value, kind: kind.value, name: name.value })
    const r = await api.get(`/connections/${conn.value.id}/ddl?${q}`)
    ddl.value = String(r.ddl || '')
    loaded = true
  } catch (e) {
    error.value = errorText(e)
  } finally {
    loading.value = false
  }
}

// 连接列表可能在标签恢复后才加载完
watch(conn, (c) => { if (c && !loaded && !loading.value) load() }, { immediate: true })

async function copy() {
  if (await copyText(ddl.value)) toast.success(t('common.copied'))
}

function openConsole() {
  if (conn.value) openers.console(useTabs(), conn.value, db.value, { sql: ddl.value })
}
</script>

<template>
  <div class="ddl-tab">
    <div class="lm-toolbar">
      <Icon :name="kind === 'table' ? 'table' : kind" :size="16" class="kind-icon" />
      <span class="title lm-ellipsis" :title="name">{{ name }}</span>
      <span class="lm-badge">{{ $t('ddl.kinds.' + kind) }}</span>
      <span v-if="db && !ui.isMobile" class="lm-faint lm-ellipsis db">{{ db }}</span>
      <span class="lm-spacer" />
      <IconBtn icon="refresh" :size="16" :label="$t('common.refresh')" :disabled="loading || !conn" @click="load" />
      <IconBtn icon="copy" :size="16" :label="$t('common.copy')" :disabled="!ddl" @click="copy" />
      <IconBtn v-if="ui.isMobile" icon="terminal" :size="16" :label="$t('ddl.openInConsole')" :disabled="!ddl" @click="openConsole" />
      <Btn v-else size="sm" variant="ghost" icon="terminal" :disabled="!ddl" @click="openConsole">{{ $t('ddl.openInConsole') }}</Btn>
    </div>
    <div class="body">
      <div v-if="connMissing" class="state"><Icon name="plug" :size="28" :stroke="1.3" /><p>{{ $t('ddl.connMissing') }}</p></div>
      <div v-else-if="error" class="state">
        <Icon name="error" :size="28" :stroke="1.3" class="err-icon" />
        <p class="err">{{ error }}</p>
        <Btn size="sm" icon="refresh" @click="load">{{ $t('common.retry') }}</Btn>
      </div>
      <div v-else-if="!loaded" class="state"><span class="spin" /><p>{{ $t('common.loading') }}</p></div>
      <div v-else-if="!ddl.trim()" class="state"><Icon name="code" :size="28" :stroke="1.3" /><p>{{ $t('ddl.empty') }}</p></div>
      <SqlView v-else :code="ddl" :dialect="dialect" fill :class="{ dim: loading }" />
    </div>
  </div>
</template>

<style scoped>
.ddl-tab { display: flex; flex-direction: column; flex: 1; min-height: 0; height: 100%; min-width: 0; }
.kind-icon { color: var(--text-2); margin: 0 2px 0 4px; }
.title { font-weight: 500; font-size: 13px; min-width: 0; }
.db { font-size: 12px; min-width: 0; max-width: 30%; }
.body { flex: 1; min-height: 0; position: relative; overflow: hidden; }
.body > .sqlview { position: absolute; inset: 0; }
.dim { opacity: .55; transition: opacity .15s; }
.state { position: absolute; inset: 0; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 10px; color: var(--text-3); padding: 24px; text-align: center; }
.state p { margin: 0; font-size: 13px; max-width: 520px; word-break: break-word; }
.state .err { color: var(--danger); }
.err-icon { color: var(--danger); }
.spin { width: 18px; height: 18px; border-radius: 50%; border: 2px solid var(--border-strong); border-right-color: transparent; animation: spin .7s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
</style>
