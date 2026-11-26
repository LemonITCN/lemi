<script setup lang="ts">
// 设计器「列」分区：电脑上是可编辑表格，手机上每列一张卡片。直接修改父组件传入的草稿。
import { computed, nextTick, ref } from 'vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import Switch from '@/components/ui/Switch.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import type { Dialect } from '@/lib/sql'
import { i18n } from '@/i18n'
import { MYSQL_CHARSETS, dropRefs, isTextType, newColumn, renameRefs, type ColDraft, type DefMode, type Draft } from './design'

const props = defineProps<{ draft: Draft; dialect: Dialect; mobile: boolean; uid: string }>()
const t = i18n.global.t
const expanded = ref<Record<string, boolean>>({})
const root = ref<HTMLElement>()
const isMy = computed(() => props.dialect === 'mysql')

const defModes = computed<{ value: DefMode; label: string }[]>(() => [
  { value: 'none', label: t('designer.def.none') },
  { value: 'null', label: 'NULL' },
  { value: 'value', label: t('designer.def.value') },
  { value: 'expr', label: t('designer.def.expr') },
])

function locked(c: ColDraft) { return !!c.generated }
// ON UPDATE 只对时间类型有意义；已经带着 ON UPDATE 的列改了类型也让用户能清掉
function isTimeType(type: string) { return /^\s*(datetime|timestamp)\b/i.test(type) }

function setName(c: ColDraft, v: string) {
  const prev = c.name
  c.name = v
  renameRefs(props.draft, prev, v)
}

function setPK(c: ColDraft, v: boolean) {
  c.primary_key = v
  if (v) c.nullable = false
}

function setDefMode(c: ColDraft, v: DefMode) {
  c.def_mode = v
  if (v === 'null') c.nullable = true
}

function move(i: number, d: number) {
  const j = i + d
  const cols = props.draft.cols
  if (j < 0 || j >= cols.length) return
  const [c] = cols.splice(i, 1)
  cols.splice(j, 0, c)
}

function remove(i: number) {
  const [c] = props.draft.cols.splice(i, 1)
  if (c) dropRefs(props.draft, c.name)
}

function uniqueName(): string {
  const names = new Set(props.draft.cols.map((c) => c.name.toLowerCase()))
  for (let n = props.draft.cols.length + 1; ; n++) if (!names.has('column_' + n)) return 'column_' + n
}

/** 新增一列（at 为插入位置，默认末尾），并聚焦到名称输入框。 */
function add(at?: number) {
  const c = newColumn(props.dialect, uniqueName())
  const i = at === undefined ? props.draft.cols.length : at
  props.draft.cols.splice(i, 0, c)
  nextTick(() => {
    const el = root.value?.querySelector<HTMLInputElement>(`[data-col="${c.key}"]`)
    el?.focus()
    el?.select()
  })
}

function hasDetail(c: ColDraft) { return isMy.value || !!c.generated }

function menu(c: ColDraft, i: number): MenuItem[] {
  const items: MenuItem[] = []
  if (props.mobile) {
    items.push({ label: t('designer.moveUp'), icon: 'arrow-up', disabled: i === 0 || locked(c), run: () => move(i, -1) })
    items.push({ label: t('designer.moveDown'), icon: 'arrow-down', disabled: i === props.draft.cols.length - 1 || locked(c), run: () => move(i, 1) })
  }
  if (hasDetail(c)) items.push({ label: t('designer.details'), icon: 'settings', checked: !!expanded.value[c.key], run: () => { expanded.value[c.key] = !expanded.value[c.key] } })
  items.push({ label: t('designer.insertBelow'), icon: 'row-add', run: () => add(i + 1) })
  items.push({ separator: true }, { label: t('common.delete'), icon: 'trash', danger: true, run: () => remove(i) })
  return items
}

// 排序规则候选按字符集过滤（数据列表由父组件按字符集生成）
const collations = (cs: string) => (MYSQL_CHARSETS.includes(cs) ? `${props.uid}-coll-${cs}` : `${props.uid}-coll`)

defineExpose({ add })
</script>

<template>
  <div ref="root" class="cols">
    <!-- 电脑：可编辑表格（窄屏时横向滚动） -->
    <div v-if="!mobile" class="scroll-x">
      <table class="grid" :class="{ my: isMy }">
        <thead>
          <tr>
            <th class="c-num">#</th>
            <th class="c-name">{{ $t('designer.col.name') }}</th>
            <th class="c-type">{{ $t('designer.col.type') }}</th>
            <th class="c-ck">{{ $t('designer.col.nullable') }}</th>
            <th class="c-def">{{ $t('designer.col.default') }}</th>
            <th class="c-ck">
              {{ $t('designer.col.autoInc') }}
              <InfoTip v-if="!isMy" :text="$t('designer.sqliteAutoIncTip')" />
            </th>
            <th class="c-ck">{{ $t('designer.col.pk') }}</th>
            <th v-if="isMy" class="c-comment">{{ $t('designer.col.comment') }}</th>
            <th class="c-act" />
          </tr>
        </thead>
        <tbody>
          <template v-for="(c, i) in draft.cols" :key="c.key">
            <tr :class="{ 'is-new': !c.orig_name, locked: locked(c) }">
              <td class="c-num">
                <span class="num">{{ i + 1 }}</span>
              </td>
              <td class="c-name">
                <div class="name-cell">
                  <input class="ci" :data-col="c.key" :value="c.name" :disabled="locked(c)" spellcheck="false" @input="setName(c, ($event.target as HTMLInputElement).value)" />
                  <span v-if="c.orig_name && c.orig_name !== c.name" class="renamed lm-ellipsis" :title="c.orig_name">{{ c.orig_name }}</span>
                  <span v-if="c.generated" class="lm-badge">{{ $t('designer.generated') }}</span>
                </div>
              </td>
              <td class="c-type"><input v-model="c.type" class="ci mono" :list="uid + '-types'" :disabled="locked(c)" spellcheck="false" /></td>
              <td class="c-ck"><input type="checkbox" class="ck" :checked="c.nullable" :disabled="c.primary_key || locked(c)" @change="c.nullable = ($event.target as HTMLInputElement).checked" /></td>
              <td class="c-def">
                <div class="def">
                  <select class="ci sel" :value="c.def_mode" :disabled="locked(c)" @change="setDefMode(c, ($event.target as HTMLSelectElement).value as DefMode)">
                    <option v-for="m in defModes" :key="m.value" :value="m.value">{{ m.label }}</option>
                  </select>
                  <input v-if="c.def_mode === 'value' || c.def_mode === 'expr'" v-model="c.def_value" class="ci" :class="{ mono: c.def_mode === 'expr' }"
                    :placeholder="c.def_mode === 'expr' ? 'CURRENT_TIMESTAMP' : ''" :disabled="locked(c)" spellcheck="false" />
                </div>
              </td>
              <td class="c-ck"><input v-model="c.auto_increment" type="checkbox" class="ck" :disabled="locked(c)" /></td>
              <td class="c-ck"><input type="checkbox" class="ck" :checked="c.primary_key" :disabled="locked(c)" @change="setPK(c, ($event.target as HTMLInputElement).checked)" /></td>
              <td v-if="isMy" class="c-comment"><input v-model="c.comment" class="ci" :disabled="locked(c)" /></td>
              <td class="c-act">
                <div class="acts">
                  <IconBtn icon="arrow-up" :size="15" :label="$t('designer.moveUp')" :disabled="i === 0 || locked(c)" @click="move(i, -1)" />
                  <IconBtn icon="arrow-down" :size="15" :label="$t('designer.moveDown')" :disabled="i === draft.cols.length - 1 || locked(c)" @click="move(i, 1)" />
                  <Menu :items="menu(c, i)" align="end"><IconBtn icon="more" :size="15" :label="$t('common.more')" /></Menu>
                </div>
              </td>
            </tr>
            <tr v-if="expanded[c.key]" class="detail">
              <td />
              <td :colspan="isMy ? 8 : 7">
                <div class="detail-grid">
                  <template v-if="isMy">
                    <label class="lm-field"><span class="lm-label">{{ $t('designer.col.charset') }}</span>
                      <input v-model="c.charset" class="ci boxed mono" :list="uid + '-cs'" :disabled="locked(c) || !isTextType(c.type)" spellcheck="false" /></label>
                    <label class="lm-field"><span class="lm-label">{{ $t('designer.col.collation') }}</span>
                      <input v-model="c.collation" class="ci boxed mono" :list="collations(c.charset)" :disabled="locked(c) || !isTextType(c.type)" spellcheck="false" /></label>
                    <label class="lm-field"><span class="lm-label">ON UPDATE<InfoTip :text="$t('designer.onUpdateTip')" /></span>
                      <input v-model="c.on_update" class="ci boxed mono" :list="uid + '-onupd'" :disabled="locked(c) || (!isTimeType(c.type) && !c.on_update)" :placeholder="$t('designer.def.none')" spellcheck="false" /></label>
                    <label class="lm-field ck-field"><input v-model="c.invisible" type="checkbox" class="ck" :disabled="c.primary_key" /><span>{{ $t('designer.col.invisible') }}</span><InfoTip :text="$t('designer.invisibleTip')" /></label>
                  </template>
                  <div v-if="c.generated" class="lm-field full"><span class="lm-label">{{ $t('designer.generatedExpr') }} ({{ c.generated }})</span><code class="lm-mono expr">{{ c.generation_expr }}</code></div>
                  <div v-if="c.extra" class="lm-field full"><span class="lm-label">EXTRA</span><code class="lm-mono expr">{{ c.extra }}</code></div>
                </div>
              </td>
            </tr>
          </template>
        </tbody>
      </table>
    </div>

    <!-- 手机：每列一张卡片 -->
    <div v-else class="cards">
      <div v-for="(c, i) in draft.cols" :key="c.key" class="card" :class="{ 'is-new': !c.orig_name, locked: locked(c) }">
        <div class="card-head">
          <span class="num">{{ i + 1 }}</span>
          <input class="ci name" :data-col="c.key" :value="c.name" :disabled="locked(c)" spellcheck="false" :aria-label="$t('designer.col.name')" @input="setName(c, ($event.target as HTMLInputElement).value)" />
          <span v-if="c.primary_key" class="lm-badge accent">PK</span>
          <span v-if="c.generated" class="lm-badge">{{ $t('designer.generated') }}</span>
          <Menu :items="menu(c, i)" align="end"><IconBtn icon="more" :size="16" :label="$t('common.more')" /></Menu>
        </div>
        <div v-if="c.orig_name && c.orig_name !== c.name" class="renamed-m lm-ellipsis">{{ $t('designer.renamedFrom', { name: c.orig_name }) }}</div>
        <div class="card-grid">
          <label class="lm-field full"><span class="lm-label">{{ $t('designer.col.type') }}</span>
            <input v-model="c.type" class="ci boxed mono" :list="uid + '-types'" :disabled="locked(c)" spellcheck="false" /></label>
          <div class="lm-field full"><span class="lm-label">{{ $t('designer.col.default') }}</span>
            <div class="def">
              <select class="ci boxed sel" :value="c.def_mode" :disabled="locked(c)" @change="setDefMode(c, ($event.target as HTMLSelectElement).value as DefMode)">
                <option v-for="m in defModes" :key="m.value" :value="m.value">{{ m.label }}</option>
              </select>
              <input v-if="c.def_mode === 'value' || c.def_mode === 'expr'" v-model="c.def_value" class="ci boxed" :class="{ mono: c.def_mode === 'expr' }" :disabled="locked(c)" spellcheck="false" />
            </div>
          </div>
          <div class="sw-row"><span>{{ $t('designer.col.nullable') }}</span><Switch :model-value="c.nullable" :disabled="c.primary_key || locked(c)" @update:model-value="(v: boolean) => (c.nullable = v)" /></div>
          <div class="sw-row"><span>{{ $t('designer.col.pk') }}</span><Switch :model-value="c.primary_key" :disabled="locked(c)" @update:model-value="(v: boolean) => setPK(c, v)" /></div>
          <div class="sw-row"><span>{{ $t('designer.col.autoInc') }}</span><Switch v-model="c.auto_increment" :disabled="locked(c)" /></div>
          <label v-if="isMy" class="lm-field full"><span class="lm-label">{{ $t('designer.col.comment') }}</span><input v-model="c.comment" class="ci boxed" :disabled="locked(c)" /></label>
          <template v-if="expanded[c.key]">
            <template v-if="isMy">
              <label class="lm-field"><span class="lm-label">{{ $t('designer.col.charset') }}</span>
                <input v-model="c.charset" class="ci boxed mono" :list="uid + '-cs'" :disabled="locked(c) || !isTextType(c.type)" spellcheck="false" /></label>
              <label class="lm-field"><span class="lm-label">{{ $t('designer.col.collation') }}</span>
                <input v-model="c.collation" class="ci boxed mono" :list="collations(c.charset)" :disabled="locked(c) || !isTextType(c.type)" spellcheck="false" /></label>
              <label class="lm-field"><span class="lm-label">ON UPDATE</span>
                <input v-model="c.on_update" class="ci boxed mono" :list="uid + '-onupd'" :disabled="locked(c) || (!isTimeType(c.type) && !c.on_update)" :placeholder="$t('designer.def.none')" spellcheck="false" /></label>
              <div class="sw-row"><span>{{ $t('designer.col.invisible') }}</span><Switch v-model="c.invisible" :disabled="c.primary_key" /></div>
            </template>
            <div v-if="c.generated" class="lm-field full"><span class="lm-label">{{ $t('designer.generatedExpr') }}</span><code class="lm-mono expr">{{ c.generation_expr }}</code></div>
            <div v-if="c.extra" class="lm-field full"><span class="lm-label">EXTRA</span><code class="lm-mono expr">{{ c.extra }}</code></div>
          </template>
        </div>
      </div>
    </div>

    <div class="add-row"><Btn size="sm" variant="ghost" icon="plus" @click="add()">{{ $t('designer.addColumn') }}</Btn></div>
  </div>
</template>

<style scoped>
.cols { min-width: 0; }
.scroll-x { overflow-x: auto; }
.grid { width: 100%; border-collapse: collapse; font-size: 13px; min-width: 760px; table-layout: fixed; }
.grid.my { min-width: 960px; }
.grid th { text-align: left; font-weight: 500; color: var(--text-2); font-size: 12px; padding: 6px 6px; border-bottom: 1px solid var(--border); white-space: nowrap; }
.grid td { padding: 3px 4px; border-bottom: 1px solid var(--border); vertical-align: middle; }
.grid tr:hover > td { background: var(--bg-soft); }
.grid tr.detail > td { background: var(--bg-soft); }
.c-num { width: 36px; text-align: center !important; }
.c-name { width: 22%; }
.c-type { width: 17%; }
.c-ck { width: 58px; text-align: center !important; }
.c-def { width: 22%; }
.c-comment { width: 18%; }
.c-act { width: 96px; }
.num { color: var(--text-3); font-size: 12px; font-variant-numeric: tabular-nums; }
tr.is-new .num, .card.is-new .num { color: var(--accent); font-weight: 600; }
tr.locked td, .card.locked { color: var(--text-2); }
.name-cell { display: flex; align-items: center; gap: 4px; min-width: 0; }
.name-cell .ci { flex: 1; min-width: 60px; }
.renamed { font-size: 11.5px; color: var(--text-3); text-decoration: line-through; max-width: 40%; flex: none; }
.renamed-m { font-size: 12px; color: var(--text-3); margin: -2px 0 6px 28px; }
.ci { width: 100%; height: 28px; padding: 0 7px; border: 1px solid transparent; border-radius: var(--radius-s); background: transparent; outline: none; font-size: 13px; min-width: 0; }
.ci:hover:not(:disabled) { border-color: var(--border-strong); }
.ci:focus { border-color: var(--accent); background: var(--bg); box-shadow: 0 0 0 2px var(--accent-soft); }
.ci.mono { font-family: var(--font-mono); font-size: 12.5px; }
.ci:disabled { color: var(--text-3); }
.ci.boxed { height: 32px; border-color: var(--border-strong); background: var(--bg); }
.ck { width: 15px; height: 15px; accent-color: var(--accent); margin: 0; vertical-align: middle; cursor: pointer; }
.ck:disabled { cursor: default; opacity: .45; }
.def { display: flex; gap: 4px; min-width: 0; }
.def .sel { flex: 0 0 auto; width: auto; max-width: 92px; }
.def input { flex: 1; }
.acts { display: flex; align-items: center; justify-content: flex-end; gap: 0; }
.detail-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 240px)); gap: 10px 16px; padding: 8px 4px 10px; }
.detail-grid .full { grid-column: 1 / -1; }
.expr { display: block; white-space: pre-wrap; word-break: break-all; background: var(--bg-sunken); border-radius: var(--radius-s); padding: 6px 8px; color: var(--text); }
.add-row { padding: 8px 4px; }

.cards { display: flex; flex-direction: column; gap: 10px; }
.card { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 8px 10px 12px; background: var(--bg); min-width: 0; }
.card.is-new { border-color: var(--accent-soft); box-shadow: inset 3px 0 0 var(--accent); }
.card-head { display: flex; align-items: center; gap: 6px; margin-bottom: 6px; min-width: 0; }
.card-head .num { width: 22px; text-align: center; flex: none; }
.card-head .name { flex: 1; font-weight: 500; font-size: 14px; height: 32px; }
.card-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 10px 12px; }
.card-grid .full { grid-column: 1 / -1; }
.ck-field { flex-direction: row; align-items: center; gap: 6px; align-self: end; min-height: 32px; font-size: 13px; color: var(--text-2); }
.sw-row { display: flex; align-items: center; justify-content: space-between; gap: 8px; font-size: 13px; color: var(--text-2); min-height: 32px; }
</style>
