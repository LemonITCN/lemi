<script setup lang="ts">
// 个人：给智能体的自定义指令（每个会话开始时放进系统提示）。
import { onMounted, ref } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'

const toast = useToast()
const t = i18n.global.t
const text = ref('')
const busy = ref(false)
onMounted(async () => { try { text.value = (await api.get('/me/ai')).instructions || '' } catch (e) { toast.error(errorText(e)) } })
async function save() {
  busy.value = true
  try { await api.put('/me/ai', { instructions: text.value }); toast.success(t('common.saved')) } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
</script>

<template>
  <div class="lm-section">
    <h3>{{ $t('agent.me.instructions') }}</h3>
    <p class="desc">{{ $t('agent.me.instructionsDesc') }}</p>
    <textarea v-model="text" class="lm-textarea" rows="8" maxlength="8000" :placeholder="$t('agent.me.placeholder')" />
    <div class="row"><span class="n">{{ text.length }} / 8000</span><Btn variant="primary" :loading="busy" @click="save">{{ $t('common.save') }}</Btn></div>
  </div>
</template>

<style scoped>
.row { display: flex; align-items: center; justify-content: space-between; margin-top: 10px; }
.n { font-size: 12px; color: var(--text-3); }
</style>
