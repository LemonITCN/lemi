<script setup lang="ts">
// 设置 → mysqldump 工具（实例管理员）：清单里的版本与下载状态、下载 / 删除、指定服务器上已安装的客户端。
import { onMounted, ref } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import TextField from '@/components/ui/TextField.vue'
import Icon from '@/components/icons/Icon.vue'
import { api } from '@/api/client'
import { useToast } from '@/stores/toast'
import { withReauth } from '@/lib/reauth'
import { confirmDialog } from '@/lib/confirm'
import { errorText, i18n } from '@/i18n'
import { formatBytes } from './api'

const toast = useToast()
const t = i18n.global.t
const st = ref<any>(null)
const installing = ref('')
const dump = ref('')
const mysql = ref('')
const saving = ref(false)

async function load() {
  try {
    st.value = await api.get('/admin/dumptools')
    dump.value = st.value.custom?.mysqldump_path || ''
    mysql.value = st.value.custom?.mysql_path || ''
  } catch (e) { toast.error(errorText(e)) }
}
onMounted(load)

async function install(v: string) {
  installing.value = v
  try { await api.post(`/admin/dumptools/${encodeURIComponent(v)}/install`); await load() } catch (e) { toast.error(errorText(e)) } finally { installing.value = '' }
}
async function remove(v: string) {
  if (!(await confirmDialog({ title: t('backup.tools.remove'), message: t('backup.tools.removeConfirm', { v }), danger: true, okText: t('common.delete') }))) return
  try { await api.del(`/admin/dumptools/${encodeURIComponent(v)}`); await load() } catch (e) { toast.error(errorText(e)) }
}
async function saveCustom() {
  saving.value = true
  try {
    await withReauth(() => api.put('/admin/dumptools/custom', { mysqldump_path: dump.value.trim(), mysql_path: mysql.value.trim() }))
    toast.success(t('common.saved'))
    await load()
  } catch (e) { toast.error(errorText(e)) } finally { saving.value = false }
}
</script>

<template>
  <div v-if="st">
    <div class="lm-section">
      <p class="desc">{{ $t('backup.tools.intro') }}</p>
      <div class="facts">
        <span><span class="lm-faint">{{ $t('backup.tools.platform') }}</span> <b class="lm-mono">{{ st.platform || '—' }}</b></span>
        <span class="url"><span class="lm-faint">{{ $t('backup.tools.baseUrl') }}</span> <span class="lm-mono lm-ellipsis">{{ st.base_url }}</span></span>
      </div>
      <div v-if="st.platform_error" class="warn"><Icon name="warning" :size="14" />{{ st.platform_error }}</div>
      <div v-if="!st.versions.length" class="lm-card lm-empty">{{ $t('backup.tools.none') }}</div>
      <div v-else class="lm-card tbl">
        <table class="lm-table">
          <thead><tr><th>{{ $t('backup.tools.version') }}</th><th>{{ $t('backup.tools.series') }}</th><th>{{ $t('backup.tools.state') }}</th><th>{{ $t('backup.tools.size') }}</th><th /></tr></thead>
          <tbody>
            <tr v-for="v in st.versions" :key="v.version">
              <td class="lm-mono">{{ v.version }}</td>
              <td>{{ v.series }}</td>
              <td>
                <span v-if="v.installed" class="lm-badge success">{{ v.source === 'preinstalled' ? $t('backup.tools.preinstalled') : $t('backup.tools.installed') }}</span>
                <span v-else-if="v.available" class="lm-badge">{{ $t('backup.tools.available') }}</span>
                <span v-else class="lm-faint">{{ $t('backup.tools.unavailable') }}</span>
              </td>
              <td>{{ v.installed ? formatBytes(v.size_bytes) : '' }}</td>
              <td class="acts">
                <Btn v-if="v.available && !v.installed" size="sm" icon="download" :loading="installing === v.version" @click="install(v.version)">{{ installing === v.version ? $t('backup.tools.installing') : $t('backup.tools.install') }}</Btn>
                <Btn v-if="v.installed && v.source !== 'preinstalled'" size="sm" variant="ghost" @click="remove(v.version)">{{ $t('backup.tools.remove') }}</Btn>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
    <div class="lm-section">
      <h3>{{ $t('backup.tools.custom') }}</h3>
      <p class="desc">{{ $t('backup.tools.customHelp') }}</p>
      <div class="lm-form">
        <TextField v-model="dump" mono :label="$t('backup.tools.mysqldump')" placeholder="/usr/bin/mysqldump" />
        <TextField v-model="mysql" mono :label="$t('backup.tools.mysql')" placeholder="/usr/bin/mysql" />
      </div>
      <div v-if="st.custom_version" class="ok"><Icon name="success" :size="14" />{{ $t('backup.tools.customVersion', { v: st.custom_version }) }}</div>
      <div v-if="st.custom_error" class="warn"><Icon name="warning" :size="14" />{{ $t('backup.tools.customError', { e: st.custom_error }) }}</div>
      <Btn class="save" variant="primary" :loading="saving" @click="saveCustom">{{ $t('common.save') }}</Btn>
    </div>
  </div>
</template>

<style scoped>
.desc { margin: 0 0 12px; color: var(--text-2); font-size: 13px; line-height: 1.6; }
.facts { display: flex; flex-wrap: wrap; gap: 6px 20px; font-size: 12.5px; margin-bottom: 12px; }
.facts .url { display: flex; gap: 6px; min-width: 0; max-width: 100%; }
.tbl { overflow-x: auto; }
.acts { text-align: right; white-space: nowrap; }
.warn, .ok { display: flex; align-items: center; gap: 6px; font-size: 12.5px; margin: 10px 0; }
.warn { color: var(--warning); }
.ok { color: var(--success); }
.save { margin-top: 14px; }
</style>
