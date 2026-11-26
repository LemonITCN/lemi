export default {
  title: '服务器监控', titleSqlite: '数据库状态', connMissing: '连接不存在，或你已没有访问权限',
  sec: { processes: '进程', status: '状态', variables: '变量', tables: '表大小', slow: '慢查询', file: '文件状态', compile: '编译选项' },
  noMatch: '没有匹配的项', updated: '更新于 {time}', every5s: '每 5 秒', time: '时间',
  uptimeDH: '{d} 天 {h} 小时', uptimeHM: '{h} 小时 {m} 分', uptimeMS: '{m} 分 {s} 秒',
  // 进程
  off: '不刷新', autoRefresh: '自动刷新', filterProc: '筛选用户、主机、库、语句', hideSleep: '隐藏 Sleep', noProcesses: '没有进程',
  procCount: '{n} 个进程（共 {total} 个）', user: '用户', host: '主机', db: '库', command: '命令', timeSec: '耗时（秒）', state: '状态', statement: '语句',
  expand: '点击展开', copySql: '复制语句', killQuery: '杀查询', killConn: '杀连接',
  killQueryTitle: '终止进程 {id} 正在执行的语句？', killConnTitle: '断开进程 {id} 的连接？',
  killQueryHint: '{who} 正在执行的语句会被中止，连接保留。', killConnHint: '{who} 的连接会被断开，未提交的事务将回滚。',
  prodWarn: '这是生产连接「{name}」，操作会立即影响线上业务。', killed: '已终止进程 {id}',
  // 状态
  qps: '每秒查询（QPS）', qpsTip: '按 Questions 的增量计算，不含存储过程内部语句。',
  tps: '每秒事务（TPS）', tpsTip: '按 Com_commit 与 Com_rollback 的增量计算；自动提交的单条语句不计入。',
  connections: '连接', connected: '已连接', running: '运行中',
  hitRatio: '缓冲池命中率', hitTip: '1 − 磁盘读取次数 ÷ 逻辑读请求次数（Innodb_buffer_pool_reads / read_requests 的增量）。',
  network: '网络流量', received: '接收', sent: '发送',
  viewChart: '图表', viewTable: '数据表', keyStatus: '关键状态值', allStatus: '全部状态值', searchStatus: '搜索状态变量',
  searchVars: '搜索变量名或值', searchCompile: '搜索编译选项',
  // 表大小
  filterTables: '筛选库或表', top: '前', noTables: '没有表', sizeSum: '{n} 个对象，合计 {size}', object: '表或索引', table: '表', engine: '引擎',
  rowsEst: '行数估计', data: '数据', index: '索引', free: '碎片', total: '合计', size: '大小',
  // 慢查询
  filterSlow: '筛选库或语句', noSlow: '没有语句统计',
  slowTip: '来自 performance_schema 的语句摘要（按总耗时排序），统计自服务器启动或上次清空以来的全部执行。',
  calls: '次数', totalTime: '总耗时', avgTime: '平均', maxTime: '最长', rowsExamined: '扫描行', rowsSent: '返回行', lastSeen: '最近一次', openConsole: '在控制台打开',
  // SQLite
  fileSize: '文件大小', pages: '页数', pageSizeOf: '每页 {size}', freePages: '空闲页',
  freelistTip: '删除数据后留下的空闲页，会被后续写入复用；VACUUM 可以把它们还给文件系统。',
}
