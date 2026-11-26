export default {
  title: 'Server monitor', titleSqlite: 'Database status', connMissing: 'The connection no longer exists, or you no longer have access to it',
  sec: { processes: 'Processes', status: 'Status', variables: 'Variables', tables: 'Table sizes', slow: 'Slow queries', file: 'File', compile: 'Compile options' },
  noMatch: 'No matches', updated: 'Updated {time}', every5s: 'every 5 s', time: 'Time',
  uptimeDH: '{d}d {h}h', uptimeHM: '{h}h {m}m', uptimeMS: '{m}m {s}s',
  // 进程
  off: 'Off', autoRefresh: 'Auto refresh', filterProc: 'Filter by user, host, database, statement', hideSleep: 'Hide Sleep', noProcesses: 'No processes',
  procCount: '{n} of {total} processes', user: 'User', host: 'Host', db: 'Database', command: 'Command', timeSec: 'Time (s)', state: 'State', statement: 'Statement',
  expand: 'Click to expand', copySql: 'Copy statement', killQuery: 'Kill query', killConn: 'Kill connection',
  killQueryTitle: 'Kill the statement running in process {id}?', killConnTitle: 'Kill the connection of process {id}?',
  killQueryHint: 'The statement {who} is running will be aborted; the connection stays open.', killConnHint: 'The connection of {who} will be closed and any uncommitted transaction rolled back.',
  prodWarn: '“{name}” is a production connection; this affects live traffic immediately.', killed: 'Process {id} killed',
  // 状态
  qps: 'Queries per second', qpsTip: 'Computed from the increase of Questions; statements inside stored programs are not counted.',
  tps: 'Transactions per second', tpsTip: 'Computed from Com_commit plus Com_rollback; autocommitted single statements are not counted.',
  connections: 'Connections', connected: 'Connected', running: 'Running',
  hitRatio: 'Buffer pool hit ratio', hitTip: '1 − disk reads ÷ logical read requests (increase of Innodb_buffer_pool_reads / read_requests).',
  network: 'Network', received: 'Received', sent: 'Sent',
  viewChart: 'Charts', viewTable: 'Table', keyStatus: 'Key status values', allStatus: 'All status values', searchStatus: 'Search status variables',
  searchVars: 'Search variable names or values', searchCompile: 'Search compile options',
  // 表大小
  filterTables: 'Filter by database or table', top: 'Top', noTables: 'No tables', sizeSum: '{n} objects, {size} in total', object: 'Table or index', table: 'Table', engine: 'Engine',
  rowsEst: 'Rows (est.)', data: 'Data', index: 'Index', free: 'Free', total: 'Total', size: 'Size',
  // 慢查询
  filterSlow: 'Filter by database or statement', noSlow: 'No statement statistics',
  slowTip: 'Statement digests from performance_schema, sorted by total time, covering every execution since the server started or the table was truncated.',
  calls: 'Calls', totalTime: 'Total', avgTime: 'Avg', maxTime: 'Max', rowsExamined: 'Rows examined', rowsSent: 'Rows sent', lastSeen: 'Last seen', openConsole: 'Open in console',
  // SQLite
  fileSize: 'File size', pages: 'Pages', pageSizeOf: '{size} per page', freePages: 'Free pages',
  freelistTip: 'Pages left free after deletes; later writes reuse them. VACUUM returns them to the file system.',
}
