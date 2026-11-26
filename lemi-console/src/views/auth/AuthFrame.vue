<script setup lang="ts">
import Logo from '@/components/brand/Logo.vue'
import { useSession } from '@/stores/session'
defineProps<{ title: string; subtitle?: string; width?: number }>()
const session = useSession()
</script>

<template>
  <div class="auth">
    <div v-if="session.insecure" class="insecure">{{ $t('auth.insecure') }}</div>
    <div class="card" :style="{ maxWidth: (width || 380) + 'px' }">
      <div class="brand"><Logo :size="32" /><span class="name">Lemi</span></div>
      <h1>{{ title }}</h1>
      <p v-if="subtitle" class="sub">{{ subtitle }}</p>
      <slot />
    </div>
    <div class="ver">Lemi {{ session.version }}</div>
  </div>
</template>

<style scoped>
.auth { min-height: 100%; display: flex; flex-direction: column; align-items: center; justify-content: center; padding: 24px 16px; background: var(--bg-soft); gap: 16px; }
.insecure { max-width: 520px; background: var(--danger-bg); color: var(--danger); border-radius: var(--radius-m); padding: 10px 14px; font-size: 13px; }
.card { width: 100%; background: var(--bg); border: 1px solid var(--border); border-radius: var(--radius-l); padding: 28px; box-shadow: var(--shadow-soft); }
.brand { display: flex; align-items: center; gap: 10px; margin-bottom: 20px; }
.name { font-size: 18px; font-weight: 650; letter-spacing: .2px; }
h1 { font-size: 18px; margin: 0 0 6px; font-weight: 600; }
.sub { margin: 0 0 18px; color: var(--text-2); font-size: 13px; line-height: 1.6; }
.ver { color: var(--text-3); font-size: 12px; }
@media (max-width: 760px) { .card { padding: 22px 18px; border: 0; box-shadow: none; } .auth { background: var(--bg); justify-content: flex-start; padding-top: 48px; } }
</style>
