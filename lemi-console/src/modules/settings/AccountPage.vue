<script setup lang="ts">
import { reactive, ref } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Segmented from '@/components/ui/Segmented.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useUI } from '@/stores/ui'
import { useToast } from '@/stores/toast'
import { errorText, i18n, setLocale } from '@/i18n'

const session = useSession()
const ui = useUI()
const toast = useToast()
const t = i18n.global.t
const f = reactive({ display_name: session.user?.display_name || '', email: session.user?.email || '' })
const busy = ref(false)
const locale = ref(i18n.global.locale.value)

async function save() {
  busy.value = true
  try {
    await api.patch('/me', { ...f, locale: locale.value })
    setLocale(locale.value)
    await session.load()
    toast.success(t('common.saved'))
  } catch (e) {
    toast.error(errorText(e))
  } finally {
    busy.value = false
  }
}
const accents = ['blue', 'green', 'purple', 'orange', 'pink', 'lemon', 'graphite']
</script>

<template>
  <div class="lm-section">
    <h3>{{ $t('settings.profile') }}</h3>
    <div class="lm-form">
      <TextField :model-value="session.user?.username" :label="$t('auth.username')" disabled />
      <TextField v-model="f.display_name" :label="$t('auth.displayName')" />
      <TextField v-model="f.email" :label="$t('auth.email')" class="full" />
      <div class="lm-field">
        <span class="lm-label">{{ $t('settings.language') }}</span>
        <Segmented v-model="locale" :options="[{ value: 'zh-CN', label: '中文' }, { value: 'en', label: 'English' }]" />
      </div>
    </div>
    <div class="actions"><Btn variant="primary" :loading="busy" @click="save">{{ $t('common.save') }}</Btn></div>
  </div>
  <div class="lm-section">
    <h3>{{ $t('settings.appearance') }}</h3>
    <div class="lm-form">
      <div class="lm-field">
        <span class="lm-label">{{ $t('shell.theme') }}</span>
        <Segmented v-model="ui.theme" :options="[{ value: 'system', label: $t('shell.themeSystem') }, { value: 'light', label: $t('shell.themeLight') }, { value: 'dark', label: $t('shell.themeDark') }]" />
      </div>
      <div class="lm-field">
        <span class="lm-label">{{ $t('settings.density') }}</span>
        <Segmented v-model="ui.density" :options="[{ value: 'compact', label: $t('settings.compact') }, { value: 'normal', label: $t('settings.normal') }, { value: 'comfortable', label: $t('settings.comfortable') }]" />
      </div>
      <div class="lm-field full">
        <span class="lm-label">{{ $t('settings.accent') }}</span>
        <div class="accents"><button v-for="a in accents" :key="a" class="acc" :class="{ on: ui.accent === a }" :data-accent="a" @click="ui.accent = a" /></div>
      </div>
    </div>
  </div>
</template>

<style scoped>
.actions { margin-top: 16px; }
.accents { display: flex; gap: 8px; }
.acc { width: 24px; height: 24px; border-radius: 50%; border: 2px solid transparent; }
.acc.on { border-color: var(--text); }
.acc[data-accent="blue"] { background: #2f6fde; } .acc[data-accent="green"] { background: #2f8a5b; } .acc[data-accent="purple"] { background: #7b5bd6; }
.acc[data-accent="orange"] { background: #d9730d; } .acc[data-accent="pink"] { background: #c94f8b; } .acc[data-accent="lemon"] { background: #b59a12; } .acc[data-accent="graphite"] { background: #4a4a48; }
</style>
