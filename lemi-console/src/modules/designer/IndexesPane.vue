<script setup lang="ts">
// 设计器「索引」分区：每个索引一张卡片（名称、类型、列与前缀长度、注释）。主键在「列」分区里设置。
import { computed } from 'vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import type { Dialect } from '@/lib/sql'
import { i18n } from '@/i18n'
import { newIndex, type Draft, type IdxDraft, type IdxKind } from './design'

const props = defineProps<{ draft: Draft; dialect: Dialect; mobile: boolean }>()
const t = i18n.global.t
const isMy = computed(() => props.dialect === 'mysql')
const kinds = computed<{ value: IdxKind; label: string }[]>(() => {
  const base: { value: IdxKind; label: string }[] = [{ value: 'index', label: t('designer.idxKind.index') }, { value: 'unique', label: t('designer.idxKind.unique') }]
  if (isMy.value) base.push({ value: 'fulltext', label: t('designer.idxKind.fulltext') }, { value: 'spatial', label: t('designer.idxKind.spatial') })
  return base
})
const colNames = computed(() => props.draft.cols.map((c) => c.name).filter(Boolean))

function autoName(ix: IdxDraft) {
  const prefix = ix.kind === 'unique' ? 'uk_' : ix.kind === 'fulltext' ? 'ft_' : 'idx_'
  return (prefix + ix.columns.map((c) => c.name).join('_')).slice(0, 64)
}

function addCol(ix: IdxDraft, name: string) {
  if (!name) return
  const wasAuto = !ix.name || ix.name === autoName(ix)
  ix.columns.push({ name, length: '' })
  if (wasAuto) ix.name = autoName(ix)
}

function moveCol(ix: IdxDraft, i: number, d: number) {
  const j = i + d
  if (j < 0 || j >= ix.columns.length) return
  const [c] = ix.columns.splice(i, 1)
  ix.columns.splice(j, 0, c)
}

function removeCol(ix: IdxDraft, i: number) { ix.columns.splice(i, 1) }

function add() { props.draft.idx.push(newIndex()) }
function remove(i: number) { props.draft.idx.splice(i, 1) }

defineExpose({ add })
</script>

<template>
  <div class="idx">
    <div v-if="!draft.idx.length" class="lm-empty">{{ $t('designer.noIndexes') }}</div>
    <div v-for="(ix, i) in draft.idx" :key="ix.key" class="card" :class="{ locked: !!ix.locked, 'is-new': !ix.orig_name }">
      <div class="head">
        <input v-model="ix.name" class="ci boxed name mono" :placeholder="$t('designer.idxName')" :disabled="!!ix.locked" spellcheck="false" :aria-label="$t('designer.idxName')" />
        <select v-model="ix.kind" class="ci boxed kind" :disabled="!!ix.locked" :aria-label="$t('designer.idxType')">
          <option v-for="k in kinds" :key="k.value" :value="k.value">{{ k.label }}</option>
        </select>
        <span v-if="ix.locked" class="lm-badge">{{ ix.locked === 'auto' ? $t('designer.idxAuto') : $t('designer.idxExpr') }}
          <InfoTip :text="ix.locked === 'auto' ? $t('designer.idxAutoTip') : $t('designer.idxExprTip')" /></span>
        <span class="lm-spacer" />
        <IconBtn v-if="ix.locked !== 'auto'" icon="trash" :size="16" :label="$t('common.delete')" @click="remove(i)" />
      </div>
      <div class="cols">
        <div v-for="(c, ci) in ix.columns" :key="ci" class="col-row">
          <span class="ord">{{ ci + 1 }}</span>
          <select v-model="c.name" class="ci boxed col-sel mono" :disabled="!!ix.locked">
            <option v-if="!colNames.includes(c.name)" :value="c.name">{{ c.name }}</option>
            <option v-for="n in colNames" :key="n" :value="n">{{ n }}</option>
          </select>
          <input v-if="isMy && ix.kind !== 'fulltext' && ix.kind !== 'spatial'" v-model="c.length" class="ci boxed len" inputmode="numeric" :placeholder="$t('designer.prefixLen')"
            :disabled="!!ix.locked" :aria-label="$t('designer.prefixLen')" />
          <template v-if="!ix.locked">
            <IconBtn icon="arrow-up" :size="15" :label="$t('designer.moveUp')" :disabled="ci === 0" @click="moveCol(ix, ci, -1)" />
            <IconBtn icon="arrow-down" :size="15" :label="$t('designer.moveDown')" :disabled="ci === ix.columns.length - 1" @click="moveCol(ix, ci, 1)" />
            <IconBtn icon="close" :size="15" :label="$t('common.remove')" @click="removeCol(ix, ci)" />
          </template>
        </div>
        <div v-if="!ix.locked" class="col-row">
          <span class="ord" />
          <select class="ci boxed col-sel add-sel" :value="''" @change="addCol(ix, ($event.target as HTMLSelectElement).value); ($event.target as HTMLSelectElement).value = ''">
            <option value="" disabled>{{ $t('designer.addIdxColumn') }}</option>
            <option v-for="n in colNames" :key="n" :value="n">{{ n }}</option>
          </select>
        </div>
      </div>
      <label v-if="isMy" class="comment"><span class="lm-label">{{ $t('designer.col.comment') }}</span><input v-model="ix.comment" class="ci boxed" :disabled="!!ix.locked" /></label>
    </div>
    <div class="add-row"><Btn size="sm" variant="ghost" icon="plus" @click="add">{{ $t('designer.addIndex') }}</Btn></div>
  </div>
</template>

<style scoped>
.idx { display: flex; flex-direction: column; gap: 10px; max-width: 880px; min-width: 0; }
.card { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 10px 12px 12px; background: var(--bg); min-width: 0; }
.card.is-new { box-shadow: inset 3px 0 0 var(--accent); }
.card.locked { background: var(--bg-soft); }
.head { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; min-width: 0; }
.head .name { flex: 1 1 200px; min-width: 0; }
.head .kind { flex: 0 0 auto; width: auto; }
.cols { display: flex; flex-direction: column; gap: 6px; margin-top: 10px; }
.col-row { display: flex; align-items: center; gap: 4px; min-width: 0; }
.ord { width: 20px; flex: none; text-align: center; font-size: 12px; color: var(--text-3); font-variant-numeric: tabular-nums; }
.col-sel { flex: 1 1 auto; min-width: 0; max-width: 320px; }
.add-sel { color: var(--text-2); }
.len { width: 84px; flex: none; }
.comment { display: flex; flex-direction: column; margin-top: 10px; max-width: 520px; }
.ci { height: 32px; padding: 0 8px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); background: var(--bg); outline: none; font-size: 13px; width: 100%; min-width: 0; }
.ci:focus { border-color: var(--accent); box-shadow: 0 0 0 2px var(--accent-soft); }
.ci.mono { font-family: var(--font-mono); font-size: 12.5px; }
.ci:disabled { color: var(--text-2); background: var(--bg-soft); }
.add-row { padding: 2px 0; }
</style>
