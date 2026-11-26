<script setup lang="ts">
// 工作空间的 AI 设置：启用哪些模型、空间默认模型、空间约定（放进每个会话的系统提示）。查看所有人可见，修改要空间管理员。
import { computed, onMounted, ref, watch } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'

const session = useSession()
const toast = useToast()
const t = i18n.global.t
const all = ref<{ ref: string; name: string; provider_name: string; context: number }[]>([])
const enabled = ref<string[]>([])
const restrict = ref(false)
const def = ref('')
const instructions = ref('')
const busy = ref(false)
const canEdit = computed(() => session.atLeast('admin'))

async function load() {
  const ws = session.current?.id
  if (!ws) return
  try {
    const [s, m] = await Promise.all([api.get(`/workspaces/${ws}/ai/settings`), api.get(`/workspaces/${ws}/ai/models?all=1`)])
    enabled.value = s.models || []
    restrict.value = enabled.value.length > 0
    def.value = s.default_model || ''
    instructions.value = s.instructions || ''
    // 已启用列表不受限制时 models 就是全部可用模型
    all.value = m.models || []
  } catch (e) { toast.error(errorText(e)) }
}
onMounted(load)
watch(() => session.current?.id, load)

function toggle(ref: string) {
  enabled.value = enabled.value.includes(ref) ? enabled.value.filter((x) => x !== ref) : [...enabled.value, ref]
}

async function save() {
  busy.value = true
  try {
    await api.put(`/workspaces/${session.current!.id}/ai/settings`, { models: restrict.value ? enabled.value : [], default_model: def.value, instructions: instructions.value })
    toast.success(t('common.saved'))
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
</script>

<template>
  <div class="lm-section">
    <h3>{{ $t('agent.ws.models') }} <InfoTip :text="$t('agent.ws.modelsHint')" /></h3>
    <label class="chk"><input v-model="restrict" type="checkbox" :disabled="!canEdit" />{{ $t('agent.ws.restrict') }}</label>
    <div v-if="restrict" class="lm-card models">
      <label v-for="m in all" :key="m.ref" class="m"><input type="checkbox" :checked="enabled.includes(m.ref)" :disabled="!canEdit" @change="toggle(m.ref)" />
        <span>{{ m.name }}</span><span class="pv">{{ m.provider_name }}</span></label>
      <div v-if="!all.length" class="lm-empty">{{ $t('agent.noModels') }}</div>
    </div>
  </div>
  <div class="lm-section">
    <h3>{{ $t('agent.ws.default') }}</h3>
    <select v-model="def" class="lm-select sel" :disabled="!canEdit">
      <option value="">{{ $t('agent.ws.followInstance') }}</option>
      <option v-for="m in all" :key="m.ref" :value="m.ref">{{ m.provider_name }} · {{ m.name }}</option>
    </select>
  </div>
  <div class="lm-section">
    <h3>{{ $t('agent.ws.instructions') }}</h3>
    <p class="desc">{{ $t('agent.ws.instructionsDesc') }}</p>
    <textarea v-model="instructions" class="lm-textarea" rows="8" maxlength="8000" :disabled="!canEdit" :placeholder="$t('agent.ws.placeholder')" />
  </div>
  <Btn v-if="canEdit" variant="primary" :loading="busy" @click="save">{{ $t('common.save') }}</Btn>
</template>

<style scoped>
.chk { display: flex; align-items: center; gap: 8px; font-size: 13px; margin: 6px 0 10px; }
.models { padding: 6px; display: flex; flex-direction: column; max-height: 320px; overflow: auto; }
.m { display: flex; align-items: center; gap: 8px; padding: 5px 6px; font-size: 13px; border-radius: var(--radius-s); }
.m:hover { background: var(--bg-hover); }
.pv { color: var(--text-3); font-size: 12px; margin-left: auto; }
.sel { max-width: 420px; }
</style>
