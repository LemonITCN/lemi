<script setup lang="ts">
// 设计器「外键」分区：每个外键一张卡片（名称、引用表、列对、ON DELETE / ON UPDATE）。
// 引用表的列在选中引用表后按需读取；引用本表时用草稿里的列。
import { computed, reactive, watch } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import type { Dialect } from '@/lib/sql'
import { fetchTable } from '@/lib/meta'
import { errorText } from '@/i18n'
import { FK_ACTIONS, newFk, type Draft, type FkDraft } from './design'

const props = defineProps<{ draft: Draft; dialect: Dialect; mobile: boolean; connId: string; database: string; origTable: string; tables: string[] }>()

const isMy = computed(() => props.dialect === 'mysql')
const colNames = computed(() => props.draft.cols.map((c) => c.name).filter(Boolean))
const actions = computed(() => FK_ACTIONS[props.dialect])

/** 引用表的列：表名 → 列名列表 / 错误。 */
const refCols = reactive<Record<string, { cols: string[]; pk: string[]; error?: string; loading?: boolean }>>({})

function refKey(f: FkDraft) { return (f.ref_database || props.database) + '\u0000' + f.ref_table }
function isSelf(f: FkDraft) {
  return (!f.ref_database || f.ref_database === props.database) && !!f.ref_table && (f.ref_table === props.origTable || f.ref_table === props.draft.opts.name)
}

function refColumns(f: FkDraft): string[] {
  if (!f.ref_table) return []
  if (isSelf(f)) return colNames.value
  return refCols[refKey(f)]?.cols || []
}

async function ensureRef(f: FkDraft) {
  if (!f.ref_table || isSelf(f)) return
  const k = refKey(f)
  if (refCols[k] && !refCols[k].error) return
  refCols[k] = { cols: [], pk: [], loading: true }
  try {
    const t = await fetchTable(props.connId, f.ref_database || props.database, f.ref_table)
    refCols[k] = { cols: t.columns.map((c) => c.name), pk: (t.indexes || []).find((x) => x.primary)?.columns || [] }
  } catch (e) {
    refCols[k] = { cols: [], pk: [], error: errorText(e) }
  }
}

// 载入已有外键时预取引用表的列
watch(() => props.draft.fks.map((f) => refKey(f)).join('|'), () => { for (const f of props.draft.fks) ensureRef(f) }, { immediate: true })

const tableOptions = (f: FkDraft) => {
  const list = [...props.tables]
  if (f.ref_table && !list.includes(f.ref_table)) list.unshift(f.ref_table)
  return list
}

function refLabel(f: FkDraft, name: string) {
  return f.ref_table === name && f.ref_database && f.ref_database !== props.database ? `${f.ref_database}.${name}` : name
}

function autoName(f: FkDraft) { return `fk_${props.draft.opts.name || props.origTable}_${f.ref_table}`.slice(0, 64) }

async function setRefTable(f: FkDraft, name: string) {
  const wasAuto = !f.name || f.name === autoName(f)
  f.ref_table = name
  f.ref_database = props.database
  for (const p of f.pairs) p.ref_column = ''
  if (wasAuto && isMy.value) f.name = autoName(f)
  await ensureRef(f)
  // 引用表的主键自动填到第一对
  const pk = isSelf(f) ? props.draft.cols.filter((c) => c.primary_key).map((c) => c.name) : refCols[refKey(f)]?.pk || []
  pk.forEach((c, i) => {
    if (!f.pairs[i]) f.pairs.push({ column: '', ref_column: '' })
    f.pairs[i].ref_column = c
  })
}

function add() { props.draft.fks.push(newFk(props.database)) }
function remove(i: number) { props.draft.fks.splice(i, 1) }

defineExpose({ add })
</script>

<template>
  <div class="fks">
    <div v-if="!draft.fks.length" class="lm-empty">{{ $t('designer.noFks') }}</div>
    <div v-for="(f, i) in draft.fks" :key="f.key" class="card" :class="{ 'is-new': !f.orig_name }">
      <div class="head">
        <input v-if="isMy" v-model="f.name" class="ci name mono" :placeholder="$t('designer.fkName')" spellcheck="false" :aria-label="$t('designer.fkName')" />
        <label class="ref">
          <span class="lm-label">{{ $t('designer.refTable') }}</span>
          <select class="ci mono" :value="f.ref_table" @change="setRefTable(f, ($event.target as HTMLSelectElement).value)">
            <option value="" disabled>{{ $t('designer.pickTable') }}</option>
            <option v-for="n in tableOptions(f)" :key="n" :value="n">{{ refLabel(f, n) }}</option>
          </select>
        </label>
        <span class="lm-spacer" />
        <IconBtn icon="trash" :size="16" :label="$t('common.delete')" @click="remove(i)" />
      </div>
      <div class="pairs">
        <div class="pair-head"><span>{{ $t('designer.fkColumn') }}</span><span /><span>{{ $t('designer.refColumn') }}</span></div>
        <div v-for="(p, pi) in f.pairs" :key="pi" class="pair">
          <select v-model="p.column" class="ci mono">
            <option value="" disabled>—</option>
            <option v-if="p.column && !colNames.includes(p.column)" :value="p.column">{{ p.column }}</option>
            <option v-for="n in colNames" :key="n" :value="n">{{ n }}</option>
          </select>
          <Icon name="arrow-right" :size="15" class="arrow" />
          <select v-model="p.ref_column" class="ci mono" :disabled="!f.ref_table">
            <option value="" disabled>{{ refCols[refKey(f)]?.loading ? $t('common.loading') : '—' }}</option>
            <option v-if="p.ref_column && !refColumns(f).includes(p.ref_column)" :value="p.ref_column">{{ p.ref_column }}</option>
            <option v-for="n in refColumns(f)" :key="n" :value="n">{{ n }}</option>
          </select>
          <IconBtn icon="close" :size="15" :label="$t('common.remove')" :disabled="f.pairs.length === 1" @click="f.pairs.splice(pi, 1)" />
        </div>
        <p v-if="refCols[refKey(f)]?.error" class="err">{{ refCols[refKey(f)]?.error }}</p>
        <div><Btn size="sm" variant="ghost" icon="plus" @click="f.pairs.push({ column: '', ref_column: '' })">{{ $t('designer.addPair') }}</Btn></div>
      </div>
      <div class="acts">
        <label class="lm-field"><span class="lm-label">ON DELETE</span>
          <select v-model="f.on_delete" class="ci"><option value="">{{ $t('designer.fkDefault') }}</option><option v-for="a in actions" :key="a" :value="a">{{ a }}</option>
            <option v-if="f.on_delete && !actions.includes(f.on_delete)" :value="f.on_delete">{{ f.on_delete }}</option></select></label>
        <label class="lm-field"><span class="lm-label">ON UPDATE</span>
          <select v-model="f.on_update" class="ci"><option value="">{{ $t('designer.fkDefault') }}</option><option v-for="a in actions" :key="a" :value="a">{{ a }}</option>
            <option v-if="f.on_update && !actions.includes(f.on_update)" :value="f.on_update">{{ f.on_update }}</option></select></label>
      </div>
    </div>
    <div class="add-row"><Btn size="sm" variant="ghost" icon="plus" @click="add">{{ $t('designer.addFk') }}</Btn></div>
  </div>
</template>

<style scoped>
.fks { display: flex; flex-direction: column; gap: 10px; max-width: 880px; min-width: 0; }
.card { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 10px 12px 12px; background: var(--bg); min-width: 0; }
.card.is-new { box-shadow: inset 3px 0 0 var(--accent); }
.head { display: flex; align-items: flex-end; gap: 10px; flex-wrap: wrap; min-width: 0; }
.head .name { flex: 1 1 220px; }
.ref { display: flex; flex-direction: column; flex: 1 1 220px; min-width: 0; }
.ref .lm-label { margin-bottom: 4px; }
.pairs { margin-top: 12px; display: flex; flex-direction: column; gap: 6px; }
.pair-head, .pair { display: grid; grid-template-columns: minmax(0, 1fr) 20px minmax(0, 1fr) 28px; gap: 6px; align-items: center; max-width: 620px; }
.pair-head span { font-size: 12px; color: var(--text-2); }
.arrow { color: var(--text-3); justify-self: center; }
.err { margin: 0; font-size: 12.5px; color: var(--danger); }
.acts { display: grid; grid-template-columns: repeat(2, minmax(0, 200px)); gap: 10px 14px; margin-top: 12px; }
.ci { height: 32px; padding: 0 8px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); background: var(--bg); outline: none; font-size: 13px; width: 100%; min-width: 0; }
.ci:focus { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.ci.mono { font-family: var(--font-mono); font-size: 12.5px; }
.ci:disabled { color: var(--text-3); background: var(--bg-soft); }
.add-row { padding: 2px 0; }
@media (max-width: 760px) { .acts { grid-template-columns: 1fr 1fr; } }
</style>
