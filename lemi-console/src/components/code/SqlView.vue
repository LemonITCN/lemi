<script setup lang="ts">
// 只读 SQL 代码视图（高亮、可选中复制）。DDL 标签、提交前 SQL 预览、设计器预览共用。
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { EditorView } from '@codemirror/view'
import { createReadonlyView, setDoc } from '@/lib/cm'
import type { Dialect } from '@/lib/sql'

const props = withDefaults(defineProps<{ code: string; dialect?: Dialect; lineNumbers?: boolean; wrap?: boolean; maxHeight?: string; fill?: boolean }>(), { dialect: 'mysql', lineNumbers: true, wrap: true })
const host = ref<HTMLDivElement>()
let view: EditorView | null = null

onMounted(() => { view = createReadonlyView(host.value!, props.code, props.dialect, { lineNumbers: props.lineNumbers, wrap: props.wrap }) })
onBeforeUnmount(() => view?.destroy())
watch(() => props.code, (c) => { if (view && view.state.doc.toString() !== c) setDoc(view, c) })
</script>

<template>
  <div ref="host" class="sqlview" :class="{ fill }" :style="maxHeight ? { maxHeight } : undefined" />
</template>

<style scoped>
.sqlview { border: 1px solid var(--border); border-radius: var(--radius-m); overflow: auto; min-height: 0; }
.sqlview.fill { border: 0; border-radius: 0; height: 100%; }
.sqlview :deep(.cm-editor) { height: auto; min-height: 100%; }
.sqlview.fill :deep(.cm-editor) { height: 100%; }
</style>
