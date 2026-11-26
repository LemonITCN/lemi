export default {
  // 菜单
  exportData: '导出数据…', exportSqlFile: '导出为 SQL 文件…', importData: '导入数据…', runSqlFile: '运行 SQL 文件…',
  copySelect: '复制 SELECT 语句', copyInsert: '复制 INSERT 语句', copiedTemplate: '已复制到剪贴板',
  truncate: '清空表…', drop: '删除表…',

  // 导出数据
  exportTitle: '导出「{name}」', exportResultTitle: '导出查询结果', format: '格式', scope: '范围', scopeFiltered: '当前筛选条件', scopeAll: '整张表',
  header: '第一行写列名', nullText: 'NULL 写成', nullTextPh: '留空表示空单元格', sqlTable: 'INSERT 的表名', maxRows: '最多行数', maxRowsPh: '不限',
  filename: '文件名', filenamePh: '默认按表名加时间', exporting: '正在准备下载…', exportStarted: '已开始下载 {file}',
  formats: { csv: 'CSV', tsv: 'TSV', xlsx: 'Excel', json: 'JSON', sql: 'SQL INSERT', markdown: 'Markdown' },
  exportHint: '文件在服务器上流式生成，大表也不会占满内存；下载链接只能用一次。',

  // 整库 SQL 导出
  dumpTitle: '导出 SQL 文件 · {db}', content: '内容', both: '结构和数据', structureOnly: '仅结构', dataOnly: '仅数据',
  objects: '表与视图', selectAll: '全选', selectNone: '全不选', selectedN: '已选 {n} / {total}', allObjectsHint: '全选时同时导出存储过程、函数、事件与触发器；只选部分表时只导出这些表及其触发器。',
  extendedInsert: '合并插入', extendedInsertHelp: '多行合并成一条 INSERT（Navicat 的「扩展插入」），文件更小、导入更快；关闭时每行一条 INSERT。',
  includeDrop: '先删除再创建', includeDropHelp: '在每个 CREATE 前写 DROP ... IF EXISTS，导入时覆盖同名对象。',
  createDatabase: '包含 CREATE DATABASE', keepDefiner: '保留 DEFINER', keepDefinerHelp: '默认去掉视图、例程、触发器、事件的 DEFINER，换个账号导入不会因为缺少 SUPER 权限失败。',
  gzip: 'gzip 压缩', noObjects: '这个库里没有表', mustSelect: '请至少选择一张表',

  // 运行 SQL 文件
  runTitle: '运行 SQL 文件', file: '文件', chooseFile: '选择文件', fileTypes: '支持 .sql 与 .sql.gz，可以是 Lemi、Navicat 或 mysqldump 导出的文件。',
  targetDb: '目标库', targetDbHelp: '文件里没有 USE 语句时，语句在这个库里执行。', onError: '遇到错误', stopOnError: '停止并回滚当前批次', continueOnError: '继续执行后面的语句',
  run: '开始运行', running: '正在运行', cancelRun: '中止', statements: '已执行 {n} 条语句', errorsN: '{n} 个错误', currentTable: '当前：{t}',
  doneOk: '运行完成：执行 {n} 条语句', doneWithErrors: '运行完成：执行 {n} 条语句，{e} 个错误', failed: '运行失败', incomplete: '文件不完整（可能被截断）',
  sourceFormat: '文件来源', formatNames: { lemi: 'Lemi', navicat: 'Navicat', mysqldump: 'mysqldump', unknown: '未识别' },
  errLine: '行', errIndex: '语句', errMsg: '错误', skipped: '跳过 {n} 条', prodRunTitle: '在生产库上运行 SQL 文件？', prodRunHint: '「{name}」标记为生产环境，文件里的语句会直接修改数据库。',
  aborted: '已中止', bytesOf: '{a} / {b}',

  // 导入数据
  importTitle: '导入数据到「{name}」', stepFile: '选择文件', stepMap: '列映射', stepDone: '完成', importFileTypes: '支持 CSV、TSV（UTF-8）与 Excel（.xlsx）。',
  sheet: '工作表', hasHeader: '第一行是列名', preview: '预览（前 {n} 行）', mapping: '列映射', tableColumn: '表的列', fileColumn: '文件的列', skipColumn: '不导入',
  colN: '第 {n} 列', autoMap: '按名称自动对应', emptyAsNull: '空单元格作为 NULL', nullMarker: '这段文字作为 NULL', nullMarkerPh: '例如 \\N 或 NULL',
  errorPolicy: '出错时', rollbackAll: '全部撤销', skipBadRows: '跳过出错的行', doImport: '导入', importing: '正在导入…',
  imported: '已导入 {n} 行', importedWithFailed: '已导入 {n} 行，跳过 {f} 行', importErrors: '出错的行', duration: '耗时 {ms} 毫秒',
  expired: '上传的文件已过期，请重新选择', noMapping: '请至少对应一列', reupload: '重新选择', generated: '生成列',

  // 清空 / 删除
  truncateTitle: '清空表「{name}」？', truncateHint: '表里的全部数据都会被删除，表结构保留。这个操作不能撤销。',
  dropTitle: '删除表「{name}」？', dropHint: '表结构与全部数据都会被删除，这个操作不能撤销。',
  typeToConfirm: '输入表名「{name}」确认：', prodDangerTitle: '这是生产库', prodDangerHint: '「{conn}」标记为生产环境。确定要在生产库上执行「{sql}」吗？',
  truncated: '已清空 {name}', dropped: '已删除 {name}',
}
