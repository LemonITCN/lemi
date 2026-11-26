<script setup lang="ts">
import { onMounted, ref } from 'vue'
import Btn from '@/components/ui/Btn.vue'
import Segmented from '@/components/ui/Segmented.vue'
import { api } from '@/api/client'
import { useSession } from '@/stores/session'
import { useToast } from '@/stores/toast'
import { errorText, i18n } from '@/i18n'

const model = defineModel<string>({ default: '' })
const session = useSession()
const toast = useToast()
const t = i18n.global.t
const mode = ref(model.value && !model.value.startsWith('upload:') ? 'server' : 'upload')
const files = ref<{ id: string; name: string; size: number }[]>([])
const dirs = ref<{ path: string; files: { path: string; size: number }[]; error?: string }[]>([])
const uploading = ref(false)
const input = ref<HTMLInputElement>()

async function load() {
  const ws = session.current!.id
  try {
    files.value = (await api.get(`/workspaces/${ws}/sqlite-files`)).files
    dirs.value = (await api.get(`/workspaces/${ws}/sqlite-dirs`)).dirs
  } catch (e) {
    toast.error(errorText(e))
  }
}
onMounted(load)

async function upload(e: Event) {
  const file = (e.target as HTMLInputElement).files?.[0]
  if (!file) return
  uploading.value = true
  try {
    const fd = new FormData()
    fd.append('file', file)
    const r = await api.post(`/workspaces/${session.current!.id}/sqlite-files`, fd)
    model.value = r.ref
    await load()
  } catch (err) {
    toast.error(errorText(err))
  } finally {
    uploading.value = false
    if (input.value) input.value.value = ''
  }
}

const size = (n: number) => (n > 1 << 20 ? (n / (1 << 20)).toFixed(1) + ' MB' : Math.ceil(n / 1024) + ' KB')
</script>

<template>
  <div class="picker">
    <Segmented v-model="mode" :options="[{ value: 'upload', label: t('conn.sqliteUploaded') }, { value: 'server', label: t('conn.sqliteServer') }]" />
    <div v-if="mode === 'upload'" class="list">
      <label v-for="f in files" :key="f.id" class="item" :class="{ on: model === 'upload:' + f.id }">
        <input v-model="model" type="radio" :value="'upload:' + f.id" /><span class="lm-ellipsis">{{ f.name }}</span><span class="meta">{{ size(f.size) }}</span>
      </label>
      <p v-if="!files.length" class="lm-faint small">{{ $t('conn.sqliteNoUploads') }}</p>
      <input ref="input" type="file" accept=".db,.sqlite,.sqlite3,.db3" hidden @change="upload" />
      <Btn size="sm" icon="upload" :loading="uploading" @click="input?.click()">{{ $t('conn.sqliteUpload') }}</Btn>
    </div>
    <div v-else class="list">
      <p v-if="!dirs.length" class="lm-faint small">{{ $t('conn.sqliteNoDirs') }}</p>
      <div v-for="d in dirs" :key="d.path">
        <div class="dir lm-mono">{{ d.path }}</div>
        <p v-if="d.error" class="err small">{{ d.error }}</p>
        <label v-for="f in d.files" :key="f.path" class="item" :class="{ on: model === f.path }">
          <input v-model="model" type="radio" :value="f.path" /><span class="lm-ellipsis lm-mono">{{ f.path.slice(d.path.length + 1) }}</span><span class="meta">{{ size(f.size) }}</span>
        </label>
      </div>
    </div>
  </div>
</template>

<style scoped>
.picker { display: flex; flex-direction: column; gap: 10px; }
.list { display: flex; flex-direction: column; gap: 4px; max-height: 220px; overflow: auto; align-items: flex-start; }
.item { display: flex; align-items: center; gap: 8px; width: 100%; padding: 5px 8px; border-radius: 6px; font-size: 13px; cursor: pointer; }
.item:hover { background: var(--bg-hover); }
.item.on { background: var(--accent-soft); }
.meta { margin-left: auto; color: var(--text-3); font-size: 11.5px; }
.dir { font-size: 12px; color: var(--text-2); margin: 6px 0 2px; }
.small { font-size: 12px; margin: 0; }
.err { color: var(--danger); }
</style>
