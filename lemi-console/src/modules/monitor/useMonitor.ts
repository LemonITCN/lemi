// 单次取数的监控项（变量、表大小、慢查询、SQLite 状态）：加载 / 错误 / 数据三种状态。
import { ref, shallowRef } from 'vue'
import { errorText } from '@/i18n'
import { fetchMonitor, type MonitorWhat } from './format'

export function useMonitor<T>(connId: () => string, what: MonitorWhat, query: () => Record<string, string | number> = () => ({})) {
  const data = shallowRef<T | null>(null)
  const at = ref(0)
  const error = ref('')
  const loading = ref(false)
  let seq = 0

  async function load() {
    const n = ++seq
    loading.value = true
    try {
      const r = await fetchMonitor<T>(connId(), what, query())
      if (n !== seq) return
      data.value = r.data
      at.value = r.at
      error.value = ''
    } catch (e) {
      if (n !== seq) return
      error.value = errorText(e)
    } finally {
      if (n === seq) loading.value = false
    }
  }

  return { data, at, error, loading, load }
}
