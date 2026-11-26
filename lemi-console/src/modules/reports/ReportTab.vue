<script setup lang="ts">
// 报告标签：阅读（图表、表格、图按保存时的快照渲染）、编辑 Markdown（左右分栏预览）、公开链接、导出。
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Tab } from '@/modules/registry'
import Icon from '@/components/icons/Icon.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Btn from '@/components/ui/Btn.vue'
import Menu, { type MenuItem } from '@/components/ui/Menu.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import RichText from '@/components/rich/RichText.vue'
import ShareDialog from './ShareDialog.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useTabs } from '@/stores/tabs'
import { useToast } from '@/stores/toast'
import { useUI } from '@/stores/ui'
import { confirmDialog } from '@/lib/confirm'
import { formatDate } from '@/lib/time'
import { errorText, i18n } from '@/i18n'
import { buildHTML, downloadText, printHTML, safeName } from './export'

const props = defineProps<{ tab: Tab; active: boolean }>()
const session = useSession()
const tabs = useTabs()
const toast = useToast()
const ui = useUI()
const t = i18n.global.t

const report = ref<any>(null)
const error = ref('')
const editing = ref(false)
const draftTitle = ref('')
const draftBody = ref('')
const saving = ref(false)
const sharing = ref(false)
const exporting = ref(false)
const id = computed(() => String(props.tab.params.reportId || ''))
const dirty = computed(() => editing.value && (draftTitle.value !== report.value?.title || draftBody.value !== report.value?.body))
const canEdit = computed(() => session.atLeast('member'))
const canManage = computed(() => report.value && (report.value.created_by === session.user?.id || session.atLeast('admin')))

async function load() {
  error.value = ''
  try {
    report.value = (await api.get(`/reports/${id.value}`)).report
    tabs.update(props.tab.id, { title: report.value.title })
  } catch (e) { error.value = errorText(e) }
}
onMounted(load)
const onChanged = (e: Event) => { if ((e as CustomEvent).detail === id.value && !editing.value) load() }
onMounted(() => window.addEventListener('lemi:report-changed', onChanged))
onBeforeUnmount(() => window.removeEventListener('lemi:report-changed', onChanged))
watch(dirty, (d) => tabs.update(props.tab.id, { dirty: d }))

function startEdit() {
  draftTitle.value = report.value.title
  draftBody.value = report.value.body
  editing.value = true
}

async function cancelEdit() {
  if (dirty.value && !(await confirmDialog({ title: t('reports.discardTitle'), message: t('reports.discardConfirm'), danger: true, okText: t('reports.discard') }))) return
  editing.value = false
}

async function save() {
  saving.value = true
  try {
    const r = await api.put(`/reports/${id.value}`, { title: draftTitle.value, body: draftBody.value })
    report.value = r.report
    tabs.update(props.tab.id, { title: r.report.title })
    editing.value = false
    if (r.warnings?.length) toast.info(r.warnings.join('；'))
    else toast.success(t('common.saved'))
    window.dispatchEvent(new CustomEvent('lemi:report-changed', { detail: '' }))
  } catch (e) { toast.error(errorText(e)) } finally { saving.value = false }
}

function onKey(e: KeyboardEvent) {
  if ((e.metaKey || e.ctrlKey) && e.key === 's') {
    e.preventDefault()
    save()
  }
}

async function exportAs(kind: 'md' | 'html' | 'pdf') {
  exporting.value = true
  try {
    if (kind === 'md') {
      const res: Response = await api.get(`/reports/${id.value}/export.md`, { raw: true })
      if (!res.ok) throw new Error(t('reports.exportFailed'))
      downloadText(safeName(report.value.title) + '.md', await res.text(), 'text/markdown')
      return
    }
    const html = await buildHTML(report.value)
    if (kind === 'html') downloadText(safeName(report.value.title) + '.html', html, 'text/html')
    else if (!printHTML(html)) toast.error(t('reports.popupBlocked'))
  } catch (e) { toast.error(errorText(e)) } finally { exporting.value = false }
}

const exportItems = computed<MenuItem[]>(() => [
  { label: t('reports.exportHTML'), icon: 'download', run: () => exportAs('html') },
  { label: t('reports.exportPDF'), icon: 'page', run: () => exportAs('pdf') },
  { label: t('reports.exportMD'), icon: 'markdown', run: () => exportAs('md') },
])

const moreItems = computed<MenuItem[]>(() => [
  { label: t('common.refresh'), icon: 'refresh', run: load },
  ...(canManage.value ? [{ separator: true }, { label: t('common.delete'), icon: 'trash', danger: true, run: remove }] : []),
])

async function remove() {
  if (!(await confirmDialog({ title: t('reports.deleteTitle'), message: t('reports.deleteConfirm', { name: report.value.title }), danger: true, okText: t('common.delete') }))) return
  try {
    await api.del(`/reports/${id.value}`)
    window.dispatchEvent(new CustomEvent('lemi:report-changed', { detail: '' }))
    tabs.close(props.tab.id)
  } catch (e) { toast.error(errorText(e)) }
}

const missing = computed(() => (report.value?.snapshot?.missing || []).length)
</script>

<template>
  <div class="rt" @keydown="onKey">
    <div class="lm-toolbar">
      <template v-if="editing">
        <input v-model="draftTitle" class="title-in" :placeholder="$t('reports.titleLabel')" />
        <span class="lm-spacer" />
        <InfoTip :width="300" :text="$t('reports.syntaxHint')" />
        <Btn size="sm" variant="ghost" @click="cancelEdit">{{ $t('common.cancel') }}</Btn>
        <Btn size="sm" variant="primary" :loading="saving" @click="save">{{ $t('common.save') }}</Btn>
      </template>
      <template v-else>
        <Icon name="report" :size="15" />
        <span class="tt lm-ellipsis">{{ report?.title }}</span>
        <span v-if="report?.share" class="lm-badge accent"><Icon name="link" :size="11" />{{ $t('reports.shared') }}</span>
        <span v-if="missing" class="lm-badge warning">{{ $t('reports.missingN', { n: missing }) }}</span>
        <span class="lm-spacer" />
        <Btn v-if="canEdit && report" size="sm" variant="ghost" icon="edit" @click="startEdit">{{ $t('common.edit') }}</Btn>
        <Btn v-if="canManage" size="sm" variant="ghost" icon="share" @click="sharing = true">{{ $t('reports.share') }}</Btn>
        <Menu v-if="report" :items="exportItems" align="end" :width="200"><Btn size="sm" variant="ghost" icon="download" :loading="exporting">{{ $t('reports.export') }}</Btn></Menu>
        <Menu :items="moreItems" align="end" :width="180"><IconBtn icon="more" :label="$t('common.more')" /></Menu>
      </template>
    </div>
    <div v-if="error" class="lm-empty err">{{ error }}</div>
    <div v-else-if="!report" class="lm-empty">{{ $t('common.loading') }}</div>
    <div v-else-if="editing" class="split" :class="{ mobile: ui.isMobile }">
      <textarea v-model="draftBody" class="editor" spellcheck="false" :placeholder="$t('reports.bodyPlaceholder')" />
      <div v-if="!ui.isMobile" class="preview"><div class="doc"><RichText :text="draftBody" :snapshot="report.snapshot" /></div></div>
    </div>
    <div v-else class="read">
      <article class="doc">
        <h1>{{ report.title }}</h1>
        <div class="meta">{{ $t('reports.updatedAt', { t: formatDate(report.updated_at, true) }) }}</div>
        <RichText v-if="report.body.trim()" :text="report.body" :snapshot="report.snapshot" />
        <div v-else class="lm-empty">{{ $t('reports.emptyBody') }}</div>
      </article>
    </div>
    <ShareDialog v-if="report" v-model:open="sharing" :report="report" @changed="load" />
  </div>
</template>

<style scoped>
.rt { flex: 1; display: flex; flex-direction: column; min-height: 0; }
.tt { font-weight: 600; font-size: 13px; min-width: 0; }
.lm-badge { gap: 3px; }
.title-in { flex: 1; min-width: 0; max-width: 520px; height: 26px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); outline: none; font-weight: 600; }
.title-in:focus { border-color: var(--accent); }
.read { flex: 1; overflow: auto; }
.doc { max-width: 860px; margin: 0 auto; padding: 32px 40px 64px; }
.doc h1 { font-size: 26px; font-weight: 700; margin: 0 0 4px; line-height: 1.3; }
.meta { font-size: 12.5px; color: var(--text-3); margin-bottom: 24px; }
.doc :deep(.lm-md) { font-size: 14.5px; }
.split { flex: 1; display: flex; min-height: 0; }
.editor { flex: 1; min-width: 0; border: 0; border-right: 1px solid var(--border); outline: none; resize: none; padding: 20px 24px; font-family: var(--font-mono); font-size: 13px; line-height: 1.65; background: var(--bg); color: var(--text); }
.split.mobile .editor { border-right: 0; }
.preview { flex: 1; min-width: 0; overflow: auto; background: var(--bg-soft); }
.preview .doc { padding: 20px 24px; }
.err { color: var(--danger); }
@media (max-width: 760px) { .doc { padding: 20px 16px 48px; } }
</style>
