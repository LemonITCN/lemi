export default {
  run: '执行', runAll: '执行全部', runHint: '执行光标所在语句或选中的 SQL（⌘/Ctrl + Enter）', cancel: '取消执行', explain: '执行计划', format: '格式化（⌘/Ctrl + Shift + F）',
  noDb: '（未选库）', begin: '开始事务', commit: '提交', rollback: '回滚', inTx: '事务中', readOnlyHint: '你对这个连接只有读取权限，或连接设为只读：会话在数据库层强制只读',
  placeholder: '在这里写 SQL，⌘/Ctrl + Enter 执行光标所在语句', result: '结果', error: '错误', exec: '执行', affected: '影响 {n} 行', fetchMore: '加载更多',
  emptyHint: '执行结果显示在这里', txLost: '会话已失效，未提交的事务已回滚', prodDanger: '在生产库执行危险语句？', prodDangerHint: '「{name}」是生产环境，语句里有 DROP、TRUNCATE 或没有 WHERE 的 UPDATE/DELETE。',
  runAnyway: '仍然执行', formatFailed: '格式化失败：', selectInConsole: '在控制台查询',
}
