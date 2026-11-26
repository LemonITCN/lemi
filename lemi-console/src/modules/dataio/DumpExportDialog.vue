<script setup lang="ts">
// 整库（或部分表）导出为 SQL 文件：自研导出器（Navicat 兼容格式），可选结构/数据、合并插入、DROP、gzip。
import { computed, onMounted, ref } from 'vue'
import Modal from '@/components/ui/Modal.vue'
import Btn from '@/components/ui/Btn.vue'
import Switch from '@/components/ui/Switch.vue'
import Segmented from '@/components/ui/Segmented.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import Icon from '@/components/icons/Icon.vue'
import { api, downloadURL } from '@/api/client'
import { useConns, type Conn } from '@/stores/conns'
import { useToast } from '@/stores/toast'
import { dialectOf } from '@/lib/dialect'
import { errorText, i18n } from '@/i18n'

const props = defineProps<{ conn: Conn; database: string; tables?: string[]; onDone: (v?: boolean) => void }>()
const conns = useConns()
const toast = useToast()
const t = i18n.global.t
const open = ref(true)
const mysql = computed(() => dialectOf(props.conn) === 'mysql')

const objects = ref<{ name: string; view: boolean }[]>([])
const picked = ref(new Set<string>())
const loading = ref(true)
const loadErr = ref('')
const filter = ref('')
const content = ref<'both' | 'structure' | 'data'>('both')
const extended = ref(true)
const drop = ref(true)
const createDb = ref(false)
const keepDefiner = ref(false)
const gzip = ref(false)
const busy = ref(false)

onMounted(async () => {
  try {
    const o = await conns.loadObjects(props.conn.id, props.database)
    objects.value = [...o.tables.map((x) => ({ name: x.name, view: false })), ...o.views.map((x) => ({ name: x.name, view: true }))]
    const pre = props.tables?.length ? props.tables : objects.value.map((x) => x.name)
    picked.value = new Set(pre.filter((n) => objects.value.some((x) => x.name === n)))
  } catch (e) {
    loadErr.value = errorText(e)
  } finally {
    loading.value = false
  }
})

const shown = computed(() => {
  const q = filter.value.trim().toLowerCase()
  return q ? objects.value.filter((x) => x.name.toLowerCase().includes(q)) : objects.value
})
const allPicked = computed(() => objects.value.length > 0 && picked.value.size === objects.value.length)

function toggle(n: string) {
  const s = new Set(picked.value)
  if (s.has(n)) s.delete(n)
  else s.add(n)
  picked.value = s
}
function pickAll(v: boolean) { picked.value = new Set(v ? objects.value.map((x) => x.name) : []) }

function close(v?: boolean) {
  open.value = false
  props.onDone(v)
}

async function run() {
  if (!picked.value.size) { toast.error(t('dataio.mustSelect')); return }
  busy.value = true
  try {
    const body: Record<string, any> = {
      databases: [mysql.value ? props.database : 'main'],
      no_data: content.value === 'structure', no_structure: content.value === 'data', no_drop: !drop.value,
      extended_insert: extended.value, gzip: gzip.value,
    }
    if (mysql.value) { body.create_database = createDb.value; body.keep_definer = keepDefiner.value }
    if (!allPicked.value) body.tables = objects.value.filter((x) => picked.value.has(x.name)).map((x) => x.name)
    const r = await api.post(`/connections/${props.conn.id}/dump/export`, body)
    downloadURL(r.url)
    toast.success(t('dataio.exportStarted', { file: r.filename }))
    close(true)
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    busy.value = false
  }
}
</script>

<template>
  <Modal :open="open" :title="$t('dataio.dumpTitle', { db: database })" :width="560" @update:open="(v: boolean) => !v && close()">
    <div class="form">
      <div class="lm-field">
        <span class="lm-label">{{ $t('dataio.content') }}</span>
        <Segmented v-model="content" :options="[{ value: 'both', label: $t('dataio.both') }, { value: 'structure', label: $t('dataio.structureOnly') }, { value: 'data', label: $t('dataio.dataOnly') }]" />
      </div>
      <div class="lm-field">
        <div class="obj-head">
          <span class="lm-label">{{ $t('dataio.objects') }}<InfoTip :text="$t('dataio.allObjectsHint')" /></span>
          <span class="lm-spacer" />
          <span class="lm-faint cnt">{{ $t('dataio.selectedN', { n: picked.size, total: objects.length }) }}</span>
          <button class="link" type="button" @click="pickAll(!allPicked)">{{ allPicked ? $t('dataio.selectNone') : $t('dataio.selectAll') }}</button>
        </div>
        <div class="objs">
          <div v-if="objects.length > 8" class="search"><Icon name="search" :size="13" /><input v-model="filter" :placeholder="$t('common.search')" spellcheck="false" /></div>
          <div v-if="loading" class="lm-empty">{{ $t('common.loading') }}</div>
          <div v-else-if="loadErr" class="err">{{ loadErr }}</div>
          <div v-else-if="!objects.length" class="lm-empty">{{ $t('dataio.noObjects') }}</div>
          <label v-for="o in shown" :key="o.name" class="obj">
            <input type="checkbox" :checked="picked.has(o.name)" @change="toggle(o.name)" />
            <Icon :name="o.view ? 'view' : 'table'" :size="14" />
            <span class="lm-ellipsis">{{ o.name }}</span>
          </label>
        </div>
      </div>
      <div class="opts">
        <label class="row"><span>{{ $t('dataio.extendedInsert') }}<InfoTip :text="$t('dataio.extendedInsertHelp')" /></span><Switch v-model="extended" :disabled="content === 'structure'" /></label>
        <label class="row"><span>{{ $t('dataio.includeDrop') }}<InfoTip :text="$t('dataio.includeDropHelp')" /></span><Switch v-model="drop" :disabled="content === 'data'" /></label>
        <template v-if="mysql">
          <label class="row"><span>{{ $t('dataio.createDatabase') }}</span><Switch v-model="createDb" /></label>
          <label class="row"><span>{{ $t('dataio.keepDefiner') }}<InfoTip :text="$t('dataio.keepDefinerHelp')" /></span><Switch v-model="keepDefiner" /></label>
        </template>
        <label class="row"><span>{{ $t('dataio.gzip') }}</span><Switch v-model="gzip" /></label>
      </div>
    </div>
    <template #footer>
      <Btn @click="close()">{{ $t('common.cancel') }}</Btn>
      <Btn variant="primary" icon="download" :loading="busy" :disabled="loading || !picked.size" @click="run">{{ $t('common.download') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.form { display: flex; flex-direction: column; gap: 16px; }
.obj-head { display: flex; align-items: center; gap: 8px; }
.obj-head .lm-label { margin-bottom: 6px; }
.cnt { font-size: 12px; margin-bottom: 6px; }
.link { border: 0; background: none; color: var(--accent); padding: 0; font-size: 12.5px; margin-bottom: 6px; }
.objs { border: 1px solid var(--border); border-radius: var(--radius-m); max-height: 220px; overflow: auto; padding: 4px; }
.search { display: flex; align-items: center; gap: 6px; padding: 0 8px; height: 28px; color: var(--text-3); border-bottom: 1px solid var(--border); margin: -4px -4px 4px; position: sticky; top: -4px; background: var(--bg-elev); }
.search input { flex: 1; border: 0; outline: none; background: none; font-size: 12.5px; min-width: 0; color: var(--text); }
.obj { display: flex; align-items: center; gap: 8px; height: 28px; padding: 0 6px; border-radius: 4px; font-size: 13px; cursor: pointer; min-width: 0; }
.obj:hover { background: var(--bg-hover); }
.obj .lm-icon { color: var(--text-2); }
.obj input { margin: 0; flex: none; }
.err { color: var(--danger); font-size: 12.5px; padding: 8px; }
.opts { display: flex; flex-direction: column; gap: 10px; }
.row { display: flex; align-items: center; justify-content: space-between; gap: 12px; font-size: 13px; }
.row > span { display: inline-flex; align-items: center; gap: 4px; }
</style>
