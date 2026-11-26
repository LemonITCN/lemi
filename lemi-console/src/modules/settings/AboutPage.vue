<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { api } from '@/api/client'
import { formatDate } from '@/lib/time'
const info = ref<any>(null)
onMounted(async () => { info.value = await api.get('/admin/system') })
</script>

<template>
  <div v-if="info" class="lm-card">
    <table class="lm-table">
      <tbody>
        <tr><td>{{ $t('settings.version') }}</td><td class="lm-mono">{{ info.version }} {{ info.commit }}</td></tr>
        <tr><td>{{ $t('settings.platform') }}</td><td class="lm-mono">{{ info.os }}/{{ info.arch }} · {{ info.go }}{{ info.in_docker ? ' · Docker' : '' }}</td></tr>
        <tr><td>{{ $t('settings.started') }}</td><td>{{ formatDate(info.started_at, true) }}</td></tr>
        <tr><td>{{ $t('settings.dataDir') }}</td><td class="lm-mono">{{ info.data_dir }}</td></tr>
        <tr><td>{{ $t('settings.listen') }}</td><td class="lm-mono">{{ info.listen }} · TLS {{ info.tls }}</td></tr>
        <tr><td>{{ $t('settings.memory') }}</td><td>{{ info.heap_mb }} MB · {{ info.goroutines }} goroutines</td></tr>
      </tbody>
    </table>
  </div>
</template>
