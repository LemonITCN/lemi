<script setup lang="ts">
// 百度网盘设备码授权：取设备码 → 显示二维码（授权页地址）与用户码 → 按间隔轮询，授权后令牌加密写回目标配置。
import { onBeforeUnmount, ref } from 'vue'
import QRCode from 'qrcode'
import Btn from '@/components/ui/Btn.vue'
import Icon from '@/components/icons/Icon.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { errorText } from '@/i18n'

const props = defineProps<{ targetId: string; authorized?: boolean }>()
const emit = defineEmits<{ done: [] }>()
const session = useSession()
const dev = ref<any>(null)
const qr = ref('')
const state = ref<'idle' | 'waiting' | 'done' | 'expired'>(props.authorized ? 'done' : 'idle')
const err = ref('')
const busy = ref(false)
let timer: ReturnType<typeof setTimeout> | null = null
let deadline = 0
let interval = 5

async function begin() {
  busy.value = true
  err.value = ''
  try {
    const d = await api.post(`/workspaces/${session.current?.id}/backups/targets/${props.targetId}/baidu/device`)
    dev.value = d
    qr.value = await QRCode.toDataURL(d.verification_url, { margin: 1, width: 168 })
    interval = Math.max(5, d.interval || 5)
    deadline = Date.now() + (d.expires_in || 300) * 1000
    state.value = 'waiting'
    schedule()
  } catch (e) { err.value = errorText(e) } finally { busy.value = false }
}
function schedule() {
  if (timer) clearTimeout(timer)
  timer = setTimeout(poll, interval * 1000)
}
async function poll() {
  if (state.value !== 'waiting') return
  if (Date.now() > deadline) { state.value = 'expired'; return }
  try {
    const r = await api.post(`/workspaces/${session.current?.id}/backups/targets/${props.targetId}/baidu/poll`, { device_code: dev.value.device_code })
    if (r.authorized) { state.value = 'done'; emit('done'); return }
    if (r.slow_down) interval += 5
    schedule()
  } catch (e) { err.value = errorText(e); state.value = 'idle' }
}
onBeforeUnmount(() => { if (timer) clearTimeout(timer) })
</script>

<template>
  <div class="ba">
    <div class="h"><Icon name="cloud" :size="15" />{{ $t('backup.targets.baidu') }}
      <span v-if="state === 'done'" class="lm-badge success">{{ $t('backup.targets.baiduAuthorized') }}</span>
    </div>
    <div v-if="state === 'waiting' && dev" class="wait">
      <img :src="qr" alt="" width="168" height="168" />
      <div class="info">
        <p>{{ $t('backup.targets.baiduScan') }}</p>
        <a :href="dev.verification_url" target="_blank" rel="noopener noreferrer" class="lm-mono">{{ dev.verification_url }}</a>
        <div class="code"><span class="lm-faint">{{ $t('backup.targets.baiduCode') }}</span><b class="lm-mono">{{ dev.user_code }}</b></div>
        <div class="lm-faint small"><span class="spin" />{{ $t('backup.targets.baiduWaiting') }}</div>
      </div>
    </div>
    <div v-else-if="state === 'expired'" class="lm-faint small">{{ $t('backup.targets.baiduExpired') }}</div>
    <div v-if="err" class="err">{{ err }}</div>
    <Btn v-if="state !== 'waiting'" size="sm" icon="key" :loading="busy" @click="begin">{{ state === 'done' ? $t('backup.targets.baiduReauthorize') : $t('backup.targets.baiduAuthorize') }}</Btn>
  </div>
</template>

<style scoped>
.ba { display: flex; flex-direction: column; gap: 10px; align-items: flex-start; padding: 12px; border: 1px solid var(--border); border-radius: var(--radius-m); margin-top: 16px; }
.h { display: flex; align-items: center; gap: 6px; font-size: 13px; font-weight: 600; }
.wait { display: flex; gap: 16px; align-items: flex-start; flex-wrap: wrap; }
.wait img { border-radius: var(--radius-m); background: #fff; padding: 4px; }
.info { display: flex; flex-direction: column; gap: 6px; font-size: 12.5px; min-width: 0; flex: 1; }
.info p { margin: 0; color: var(--text-2); }
.info a { color: var(--accent); word-break: break-all; font-size: 12px; }
.code { display: flex; align-items: center; gap: 8px; }
.code b { font-size: 18px; letter-spacing: 2px; }
.small { font-size: 12px; display: flex; align-items: center; gap: 6px; }
.spin { width: 10px; height: 10px; border: 1.5px solid var(--border-strong); border-top-color: var(--accent); border-radius: 50%; animation: sp .8s linear infinite; }
@keyframes sp { to { transform: rotate(360deg); } }
.err { color: var(--danger); font-size: 12.5px; }
</style>
