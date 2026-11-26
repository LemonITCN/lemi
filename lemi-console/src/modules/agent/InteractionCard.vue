<script setup lang="ts">
// 待处理的交互：写入审批（完整 SQL、预计影响、涉及的表与表结构、生产标记）、重复调用确认、向用户提问。
import { computed, reactive, ref } from 'vue'
import Icon from '@/components/icons/Icon.vue'
import Btn from '@/components/ui/Btn.vue'
import InfoTip from '@/components/ui/InfoTip.vue'
import SqlView from '@/components/code/SqlView.vue'
import type { Interaction } from './types'
import { useAgent } from './store'
import { useToast } from '@/stores/toast'
import { errorText } from '@/i18n'

const props = defineProps<{ it: Interaction }>()
const agent = useAgent()
const toast = useToast()
const rejecting = ref(false)
const note = ref('')
const busy = ref(false)
const showDDL = ref(false)

const isApproval = computed(() => props.it.kind === 'approval')
const dialect = computed(() => (props.it.driver === 'sqlite' ? 'sqlite' : 'mysql'))
const tables = computed(() => (props.it.tables || []).map((t: any) => [t.database, t.table].filter(Boolean).join('.')))
const ddl = computed(() => Object.entries(props.it.related_ddl || {}) as [string, string][])

async function reply(r: 'once' | 'always' | 'reject') {
  busy.value = true
  try { await agent.respond(props.it.id, r, r === 'reject' ? note.value.trim() : '') } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}

// 提问
const qs = computed<any[]>(() => props.it.questions || [])
const answers = reactive<Record<number, { picked: string[]; other: string }>>({})
qs.value.forEach((_, i) => (answers[i] = { picked: [], other: '' }))
function toggle(i: number, label: string, multiple: boolean) {
  const a = answers[i]
  if (multiple) a.picked = a.picked.includes(label) ? a.picked.filter((x) => x !== label) : [...a.picked, label]
  else a.picked = a.picked[0] === label ? [] : [label]
}
const canAnswer = computed(() => qs.value.every((_, i) => answers[i]?.picked.length || answers[i]?.other.trim()))
async function answer() {
  busy.value = true
  try {
    const list = qs.value.map((q, i) => {
      const a = answers[i]
      const vals = [...a.picked, ...(a.other.trim() ? [a.other.trim()] : [])]
      return q.multiple ? vals : vals.join('；')
    })
    await agent.respond(props.it.id, 'once', '', list)
  } catch (e) { toast.error(errorText(e)) } finally { busy.value = false }
}
</script>

<template>
  <div class="ic" :class="{ prod: it.production }">
    <!-- 审批 -->
    <template v-if="isApproval">
      <div class="head">
        <Icon name="shield" :size="15" />
        <span class="title">{{ $t('agent.approve.title') }}</span>
        <span v-if="it.production" class="lm-badge danger">{{ $t('agent.approve.prod') }}</span>
        <span class="lm-badge">{{ $t('agent.approve.class.' + it.class) }}</span>
        <span class="lm-spacer" />
        <InfoTip :text="it.reason" />
      </div>
      <div class="where"><Icon :name="it.driver === 'sqlite' ? 'file' : 'server'" :size="13" />{{ it.connection_name }}<template v-if="it.db"> · {{ it.db }}</template></div>
      <div v-if="it.explanation" class="why">{{ it.explanation }}</div>
      <SqlView :code="it.sql" :dialect="dialect" :line-numbers="false" max-height="240px" />
      <div v-if="it.dangerous" class="warn"><Icon name="warning" :size="13" />{{ it.dangerous }}</div>
      <div v-if="it.estimate" class="est">{{ it.estimate }}</div>
      <div v-if="tables.length" class="tables">{{ $t('agent.approve.tables') }}：<span class="lm-mono">{{ tables.join(', ') }}</span>
        <button v-if="ddl.length" class="link" @click="showDDL = !showDDL">{{ showDDL ? $t('agent.approve.hideDDL') : $t('agent.approve.showDDL') }}</button>
      </div>
      <div v-if="showDDL" class="ddl">
        <div v-for="[k, v] in ddl" :key="k"><div class="dk">{{ k }}</div><SqlView :code="v" :dialect="dialect" :line-numbers="false" max-height="180px" /></div>
      </div>
      <div v-if="rejecting" class="reject">
        <textarea v-model="note" class="lm-textarea" rows="2" :placeholder="$t('agent.approve.rejectHint')" />
      </div>
      <div class="btns">
        <template v-if="!rejecting">
          <Btn size="sm" variant="primary" :loading="busy" @click="reply('once')">{{ $t('agent.approve.once') }}</Btn>
          <Btn size="sm" :disabled="busy" @click="reply('always')">{{ $t('agent.approve.always') }}</Btn>
          <span class="lm-spacer" />
          <Btn size="sm" variant="ghost" :disabled="busy" @click="rejecting = true">{{ $t('agent.approve.reject') }}</Btn>
        </template>
        <template v-else>
          <Btn size="sm" variant="danger" :loading="busy" @click="reply('reject')">{{ note.trim() ? $t('agent.approve.rejectWithNote') : $t('agent.approve.rejectStop') }}</Btn>
          <Btn size="sm" variant="ghost" @click="rejecting = false">{{ $t('common.cancel') }}</Btn>
        </template>
      </div>
    </template>
    <!-- 重复调用 -->
    <template v-else-if="it.kind === 'doom_loop'">
      <div class="head"><Icon name="refresh" :size="15" /><span class="title">{{ $t('agent.doom.title') }}</span></div>
      <div class="why">{{ $t('agent.doom.desc', { tool: it.tool }) }}</div>
      <pre class="args">{{ JSON.stringify(it.args, null, 2) }}</pre>
      <div v-if="rejecting" class="reject"><textarea v-model="note" class="lm-textarea" rows="2" :placeholder="$t('agent.doom.rejectHint')" /></div>
      <div class="btns">
        <template v-if="!rejecting">
          <Btn size="sm" variant="primary" :loading="busy" @click="reply('once')">{{ $t('agent.doom.continue') }}</Btn>
          <span class="lm-spacer" />
          <Btn size="sm" variant="ghost" @click="rejecting = true">{{ $t('agent.doom.stop') }}</Btn>
        </template>
        <template v-else>
          <Btn size="sm" variant="danger" :loading="busy" @click="reply('reject')">{{ $t('common.confirm') }}</Btn>
          <Btn size="sm" variant="ghost" @click="rejecting = false">{{ $t('common.cancel') }}</Btn>
        </template>
      </div>
    </template>
    <!-- 提问 -->
    <template v-else>
      <div class="head"><Icon name="question" :size="15" /><span class="title">{{ $t('agent.ask.title') }}</span></div>
      <div v-for="(q, i) in qs" :key="i" class="q">
        <div class="qt"><span v-if="q.header" class="lm-badge accent">{{ q.header }}</span>{{ q.question }}</div>
        <div v-if="q.options?.length" class="opts">
          <button v-for="o in q.options" :key="o.label" class="opt" :class="{ on: answers[i].picked.includes(o.label) }" @click="toggle(i, o.label, !!q.multiple)">
            <span class="mark"><Icon v-if="answers[i].picked.includes(o.label)" name="check" :size="11" /></span>
            <span class="ol"><b>{{ o.label }}</b><span v-if="o.description" class="od">{{ o.description }}</span></span>
          </button>
        </div>
        <input v-model="answers[i].other" class="other" :placeholder="q.options?.length ? $t('agent.ask.other') : $t('agent.ask.input')" />
      </div>
      <div class="btns"><Btn size="sm" variant="primary" :loading="busy" :disabled="!canAnswer" @click="answer">{{ $t('agent.ask.submit') }}</Btn></div>
    </template>
  </div>
</template>

<style scoped>
.ic { border: 1px solid var(--accent); border-radius: var(--radius-l); background: var(--bg-elev); padding: 10px 12px; display: flex; flex-direction: column; gap: 8px; box-shadow: var(--shadow-soft); }
.ic.prod { border-color: var(--danger); }
.head { display: flex; align-items: center; gap: 6px; color: var(--accent); }
.ic.prod .head { color: var(--danger); }
.title { font-weight: 600; font-size: 13px; color: var(--text); }
.where { display: flex; align-items: center; gap: 5px; font-size: 12px; color: var(--text-2); }
.why { font-size: 12.5px; color: var(--text); line-height: 1.5; }
.warn { display: flex; align-items: center; gap: 5px; font-size: 12px; color: var(--danger); }
.est { font-size: 12px; color: var(--text-2); }
.tables { font-size: 12px; color: var(--text-2); }
.link { border: 0; background: none; color: var(--accent); font-size: 12px; padding: 0 4px; }
.ddl { display: flex; flex-direction: column; gap: 6px; }
.dk { font-size: 11.5px; color: var(--text-3); margin-bottom: 2px; font-family: var(--font-mono); }
.btns { display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.args { margin: 0; font-size: 11.5px; font-family: var(--font-mono); background: var(--bg-soft); border-radius: var(--radius-s); padding: 6px 8px; max-height: 160px; overflow: auto; }
.q { display: flex; flex-direction: column; gap: 6px; }
.qt { font-size: 13px; display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }
.opts { display: flex; flex-direction: column; gap: 4px; }
.opt { display: flex; align-items: flex-start; gap: 8px; text-align: left; border: 1px solid var(--border); border-radius: var(--radius-m); background: var(--bg); padding: 6px 8px; font-size: 12.5px; }
.opt:hover { border-color: var(--border-strong); }
.opt.on { border-color: var(--accent); background: var(--accent-soft); }
.mark { width: 14px; height: 14px; border: 1px solid var(--border-strong); border-radius: 3px; display: flex; align-items: center; justify-content: center; flex: none; margin-top: 2px; color: var(--accent); }
.ol { display: flex; flex-direction: column; gap: 1px; }
.od { color: var(--text-3); font-size: 11.5px; }
.other { height: 30px; border: 1px solid var(--border-strong); border-radius: var(--radius-m); padding: 0 8px; background: var(--bg); outline: none; font-size: 12.5px; }
.other:focus { border-color: var(--accent); }
</style>
