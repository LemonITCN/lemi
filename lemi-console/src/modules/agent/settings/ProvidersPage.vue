<script setup lang="ts">
// 实例管理员：模型供应商（OpenCode Go 与自定义供应商）与实例默认模型。密钥只写不读。
import { computed, onMounted, ref } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import IconBtn from '@/components/ui/IconBtn.vue'
import Icon from '@/components/icons/Icon.vue'
import Modal from '@/components/ui/Modal.vue'
import TextField from '@/components/ui/TextField.vue'
import Switch from '@/components/ui/Switch.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { confirmDialog } from '@/lib/confirm'
import { errorText, i18n } from '@/i18n'

interface ModelRow { id: string; name: string; context: number; max_output: number; vision: boolean }
interface Provider { id: string; kind: 'opencode-go' | 'custom'; name: string; base_url: string; protocol: string; models: ModelRow[]; auto_models: boolean; enabled: boolean; api_key: { set: boolean } }

const toast = useToast()
const t = i18n.global.t
const providers = ref<Provider[]>([])
const protocols = ref<string[]>(['chat-completions', 'messages', 'responses'])
const defaultModel = ref('')
const allModels = ref<{ ref: string; label: string }[]>([])
const loading = ref(true)

async function load() {
  loading.value = true
  try {
    const r = await api.get('/admin/ai')
    providers.value = r.providers
    protocols.value = r.protocols || protocols.value
    defaultModel.value = r.default_model || ''
    const lists = await Promise.all(providers.value.filter((p) => p.enabled).map(async (p) => {
      try {
        const m = await api.get(`/admin/ai/providers/${p.id}/models`)
        return (m.models || []).filter((x: any) => x.type !== 'embedding').map((x: any) => ({ ref: p.id + '/' + x.id, label: `${p.name} · ${x.name || x.id}` }))
      } catch { return [] }
    }))
    allModels.value = lists.flat()
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    loading.value = false
  }
}
onMounted(load)

const hasOpenCode = computed(() => providers.value.some((p) => p.kind === 'opencode-go'))

// ---------- 编辑 ----------
const ed = ref({ open: false, id: '', kind: 'custom' as string, name: '', base_url: '', api_key: '', protocol: 'chat-completions', auto_models: false, enabled: true, keySet: false, models: [] as ModelRow[] })
const saving = ref(false)
const testing = ref(false)
const testModel = ref('')
const testResult = ref<{ ok: boolean; text: string } | null>(null)

function openEdit(p?: Provider, kind: Provider['kind'] = 'custom') {
  Object.assign(ed.value, { open: true, id: p?.id || '', kind: p?.kind || kind, name: p?.name || (kind === 'opencode-go' ? 'OpenCode Go' : ''), base_url: p?.base_url || '',
    api_key: '', protocol: p?.protocol || 'chat-completions', auto_models: p?.auto_models ?? false, enabled: p?.enabled ?? true, keySet: !!p?.api_key.set,
    models: (p?.models || []).map((m) => ({ ...m })) })
  testResult.value = null
  testModel.value = ''
}

function addModel() { ed.value.models.push({ id: '', name: '', context: 128000, max_output: 16000, vision: false }) }

function payload() {
  return { kind: ed.value.kind, name: ed.value.name, base_url: ed.value.base_url, api_key: ed.value.api_key, protocol: ed.value.protocol, auto_models: ed.value.auto_models, enabled: ed.value.enabled,
    models: ed.value.models.filter((m) => m.id.trim()).map((m) => ({ ...m, context: Number(m.context) || 0, max_output: Number(m.max_output) || 0 })) }
}

async function save() {
  saving.value = true
  try {
    if (ed.value.id) await api.put(`/admin/ai/providers/${ed.value.id}`, payload())
    else await api.post('/admin/ai/providers', payload())
    ed.value.open = false
    toast.success(t('common.saved'))
    await load()
  } catch (e) { toast.error(errorText(e)) } finally { saving.value = false }
}

async function test() {
  testing.value = true
  testResult.value = null
  try {
    const r = await api.post('/admin/ai/providers/test', { ...payload(), provider_id: ed.value.id, model: testModel.value })
    if (!r.ok) testResult.value = { ok: false, text: r.error }
    else {
      const n = (r.models || []).length
      testResult.value = { ok: true, text: r.reply ? t('agent.prov.testReply', { n, reply: r.reply }) : t('agent.prov.testModels', { n }) }
      if (ed.value.kind === 'custom' && !ed.value.models.length && n && !ed.value.auto_models) {
        ed.value.models = r.models.slice(0, 50).map((m: any) => ({ id: m.id, name: m.name || m.id, context: m.context || 128000, max_output: m.max_output || 16000, vision: !!m.vision }))
      }
    }
  } catch (e) { testResult.value = { ok: false, text: errorText(e) } } finally { testing.value = false }
}

async function toggle(p: Provider) {
  try {
    await api.put(`/admin/ai/providers/${p.id}`, { ...p, api_key: '', enabled: !p.enabled })
    await load()
  } catch (e) { toast.error(errorText(e)) }
}

async function remove(p: Provider) {
  if (!(await confirmDialog({ title: t('agent.prov.deleteTitle'), message: t('agent.prov.deleteConfirm', { name: p.name }), danger: true, okText: t('common.delete') }))) return
  try { await api.del(`/admin/ai/providers/${p.id}`); await load() } catch (e) { toast.error(errorText(e)) }
}

async function saveDefault() {
  try { await api.put('/admin/ai/settings', { default_model: defaultModel.value }); toast.success(t('common.saved')) } catch (e) { toast.error(errorText(e)) }
}
</script>

<template>
  <div class="lm-section">
    <h3>{{ $t('agent.prov.title') }}</h3>
    <p class="desc">{{ $t('agent.prov.desc') }}</p>
    <div v-if="loading" class="lm-empty">{{ $t('common.loading') }}</div>
    <div v-else-if="!providers.length" class="lm-card lm-empty">{{ $t('agent.prov.none') }}</div>
    <div v-else class="list">
      <div v-for="p in providers" :key="p.id" class="lm-card prov" :class="{ off: !p.enabled }">
        <Icon :name="p.kind === 'opencode-go' ? 'cloud' : 'server'" :size="18" />
        <div class="info">
          <div class="pn">{{ p.name }} <span class="lm-badge">{{ p.kind === 'opencode-go' ? 'OpenCode Go' : p.protocol }}</span></div>
          <div class="ps lm-ellipsis">
            <template v-if="p.kind === 'custom'">{{ p.base_url }} · {{ p.auto_models ? $t('agent.prov.autoModels') : $t('agent.prov.modelsN', { n: p.models.length }) }}</template>
            <template v-else>{{ p.api_key.set ? $t('agent.prov.keySet') : $t('agent.prov.keyMissing') }}</template>
          </div>
        </div>
        <Switch :model-value="p.enabled" @update:model-value="toggle(p)" />
        <IconBtn icon="edit" :label="$t('common.edit')" @click="openEdit(p)" />
        <IconBtn icon="trash" :label="$t('common.delete')" @click="remove(p)" />
      </div>
    </div>
    <div class="adds">
      <Btn v-if="!hasOpenCode" icon="plus" @click="openEdit(undefined, 'opencode-go')">{{ $t('agent.prov.addOpenCode') }}</Btn>
      <Btn icon="plus" @click="openEdit(undefined, 'custom')">{{ $t('agent.prov.addCustom') }}</Btn>
    </div>
  </div>

  <div class="lm-section">
    <h3>{{ $t('agent.prov.default') }} <InfoTip :text="$t('agent.prov.defaultHint')" /></h3>
    <div class="row">
      <select v-model="defaultModel" class="lm-select">
        <option value="">{{ $t('agent.prov.autoPick') }}</option>
        <option v-for="m in allModels" :key="m.ref" :value="m.ref">{{ m.label }}</option>
      </select>
      <Btn variant="primary" @click="saveDefault">{{ $t('common.save') }}</Btn>
    </div>
  </div>

  <Modal v-model:open="ed.open" :title="ed.id ? $t('agent.prov.edit') : ed.kind === 'opencode-go' ? $t('agent.prov.addOpenCode') : $t('agent.prov.addCustom')" :width="620">
    <div class="lm-form">
      <TextField v-model="ed.name" :label="$t('common.name')" />
      <div class="lm-field">
        <span class="lm-label">{{ $t('agent.prov.key') }}<InfoTip :text="$t('agent.prov.keyHint')" /></span>
        <input v-model="ed.api_key" type="password" class="lm-select" autocomplete="new-password" :placeholder="ed.keySet ? $t('agent.prov.keyKeep') : ''" />
      </div>
      <template v-if="ed.kind === 'custom'">
        <TextField v-model="ed.base_url" class="full" :label="$t('agent.prov.baseUrl')" placeholder="https://api.example.com/v1" mono />
        <div class="lm-field">
          <span class="lm-label">{{ $t('agent.prov.protocol') }}</span>
          <select v-model="ed.protocol" class="lm-select"><option v-for="p in protocols" :key="p" :value="p">{{ $t('agent.prov.proto.' + p.replace('-', '_')) }}</option></select>
        </div>
        <div class="sw"><Switch v-model="ed.auto_models" />{{ $t('agent.prov.autoModels') }}<InfoTip :text="$t('agent.prov.autoModelsHint')" /></div>
      </template>
      <TextField v-if="ed.kind !== 'custom'" v-model="ed.base_url" class="full" :label="$t('agent.prov.modelsUrl')" placeholder="https://opencode.ai/zen/go/v1/models" mono />
    </div>
    <div v-if="ed.kind === 'custom'" class="models">
      <div class="mh"><span class="lm-label">{{ $t('agent.prov.models') }}</span><span class="lm-spacer" /><Btn size="sm" icon="plus" variant="ghost" @click="addModel">{{ $t('common.add') }}</Btn></div>
      <table v-if="ed.models.length" class="lm-table">
        <thead><tr><th>{{ $t('agent.prov.modelId') }}</th><th>{{ $t('common.name') }}</th><th>{{ $t('agent.prov.ctx') }}</th><th>{{ $t('agent.prov.vision') }}</th><th /></tr></thead>
        <tbody>
          <tr v-for="(m, i) in ed.models" :key="i">
            <td><input v-model="m.id" class="cell lm-mono" /></td>
            <td><input v-model="m.name" class="cell" /></td>
            <td><input v-model.number="m.context" class="cell num" type="number" min="0" /></td>
            <td><input v-model="m.vision" type="checkbox" /></td>
            <td><IconBtn icon="close" :size="14" :label="$t('common.remove')" @click="ed.models.splice(i, 1)" /></td>
          </tr>
        </tbody>
      </table>
    </div>
    <div class="test">
      <input v-model="testModel" class="lm-select tm" :placeholder="$t('agent.prov.testModel')" />
      <Btn :loading="testing" icon="play" @click="test">{{ $t('common.test') }}</Btn>
      <span v-if="testResult" class="tr" :class="{ bad: !testResult.ok }">{{ testResult.text }}</span>
    </div>
    <template #footer>
      <Btn @click="ed.open = false">{{ $t('common.cancel') }}</Btn>
      <Btn variant="primary" :loading="saving" @click="save">{{ $t('common.save') }}</Btn>
    </template>
  </Modal>
</template>

<style scoped>
.list { display: flex; flex-direction: column; gap: 8px; }
.prov { display: flex; align-items: center; gap: 10px; padding: 10px 12px; }
.prov.off { opacity: .6; }
.info { flex: 1; min-width: 0; }
.pn { font-weight: 500; display: flex; align-items: center; gap: 6px; }
.ps { font-size: 12px; color: var(--text-3); margin-top: 2px; }
.adds { display: flex; gap: 8px; margin-top: 12px; }
.row { display: flex; gap: 8px; max-width: 560px; }
.sw { display: flex; align-items: center; gap: 8px; font-size: 13px; color: var(--text-2); align-self: end; height: 34px; }
.models { margin-top: 16px; }
.mh { display: flex; align-items: center; margin-bottom: 4px; }
.cell { width: 100%; height: 28px; border: 1px solid var(--border); border-radius: var(--radius-s); padding: 0 6px; background: var(--bg); outline: none; font-size: 12.5px; }
.cell:focus { border-color: var(--accent); }
.cell.num { width: 96px; }
.test { display: flex; align-items: center; gap: 8px; margin-top: 16px; flex-wrap: wrap; }
.tm { width: 220px; flex: none; }
.tr { font-size: 12.5px; color: var(--success); }
.tr.bad { color: var(--danger); }
</style>
