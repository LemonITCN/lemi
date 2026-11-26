// 命令式打开对话框：对象树右键菜单、智能体工具等不在组件树里的地方用它弹出导入、导出、确认输入等对话框。
// 组件收到 props 与一个 onDone(result) 回调；调用 onDone 后对话框卸载，openDialog 的 Promise 以 result 兑现。
import { getCurrentInstance, h, render, type AppContext, type Component } from 'vue'

let appContext: AppContext | null = null

/** 在任意组件的 setup / 生命周期钩子里调用一次，记下应用上下文（i18n、pinia、路由）。 */
export function captureAppContext() {
  const inst = getCurrentInstance()
  if (inst) appContext = inst.appContext
}

export function openDialog<T = any>(comp: Component, props: Record<string, any> = {}): Promise<T | undefined> {
  return new Promise((resolve) => {
    const el = document.createElement('div')
    document.body.appendChild(el)
    let done = false
    const finish = (v?: T) => {
      if (done) return
      done = true
      // 等关闭动画与焦点归还结束后再卸载
      setTimeout(() => { render(null, el); el.remove() }, 200)
      resolve(v)
    }
    const vnode = h(comp, { ...props, onDone: finish })
    vnode.appContext = appContext
    render(vnode, el)
  })
}
