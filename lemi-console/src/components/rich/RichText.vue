<script setup lang="ts">
// 富文本：Markdown 段 v-html（已清洗）+ 引用块组件 + mermaid 图。
import { computed } from 'vue'
import { renderMarkdown, splitBlocks } from './markdown'
import LemiBlock, { type Snapshot } from './LemiBlock.vue'
import MermaidView from './MermaidView.vue'

const props = defineProps<{ text: string; snapshot?: Snapshot | null; publicView?: boolean }>()
const segs = computed(() => splitBlocks(props.text).map((s) => (s.type === 'md' ? { ...s, html: renderMarkdown(s.text) } : s)))
</script>

<template>
  <div class="lm-md">
    <template v-for="(s, i) in segs" :key="i">
      <div v-if="s.type === 'md'" v-html="(s as any).html" />
      <LemiBlock v-else-if="s.type === 'lemi'" :kind="s.kind" :id="s.id" :caption="s.caption" :snapshot="snapshot" :public-view="publicView" />
      <div v-else-if="s.type === 'mermaid'" class="mm"><MermaidView :code="s.code" /></div>
    </template>
  </div>
</template>

<style>
.lm-md { font-size: 13.5px; line-height: 1.65; color: var(--text); word-break: break-word; }
.lm-md > div > :first-child { margin-top: 0; }
.lm-md > div > :last-child { margin-bottom: 0; }
.lm-md p { margin: 0 0 .7em; }
.lm-md h1, .lm-md h2, .lm-md h3, .lm-md h4 { font-weight: 600; line-height: 1.35; margin: 1.1em 0 .5em; }
.lm-md h1 { font-size: 1.45em; }
.lm-md h2 { font-size: 1.25em; }
.lm-md h3 { font-size: 1.1em; }
.lm-md h4 { font-size: 1em; }
.lm-md ul, .lm-md ol { margin: 0 0 .7em; padding-left: 1.4em; }
.lm-md li { margin: .15em 0; }
.lm-md a { color: var(--accent); text-decoration: none; }
.lm-md a:hover { text-decoration: underline; }
.lm-md code { font-family: var(--font-mono); font-size: .88em; background: var(--code-bg); color: var(--code-text); padding: 1px 4px; border-radius: var(--radius-xs); }
.lm-md pre { background: var(--bg-soft); border: 1px solid var(--border); border-radius: var(--radius-m); padding: 8px 10px; overflow: auto; margin: 0 0 .8em; }
.lm-md pre code { background: none; color: var(--text); padding: 0; font-size: 12px; }
.lm-md blockquote { margin: 0 0 .7em; padding: 2px 12px; border-left: 3px solid var(--border-strong); color: var(--text-2); }
.lm-md table { border-collapse: collapse; margin: 0 0 .8em; font-size: 12.5px; display: block; overflow-x: auto; max-width: 100%; }
.lm-md th, .lm-md td { border: 1px solid var(--border); padding: 4px 8px; text-align: left; }
.lm-md th { background: var(--bg-soft); font-weight: 500; }
.lm-md hr { border: 0; border-top: 1px solid var(--border); margin: 1em 0; }
.lm-md .mm { border: 1px solid var(--border); border-radius: var(--radius-l); padding: 8px; margin: 10px 0; }
</style>
