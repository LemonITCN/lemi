export default {
  exportData: 'Export data…', exportSqlFile: 'Export as SQL file…', importData: 'Import data…', runSqlFile: 'Run SQL file…',
  copySelect: 'Copy SELECT statement', copyInsert: 'Copy INSERT statement', copiedTemplate: 'Copied to clipboard',
  truncate: 'Truncate table…', drop: 'Drop table…',

  exportTitle: 'Export “{name}”', exportResultTitle: 'Export query result', format: 'Format', scope: 'Rows', scopeFiltered: 'Current filter', scopeAll: 'Whole table',
  header: 'Column names in first row', nullText: 'Write NULL as', nullTextPh: 'Empty cell when blank', sqlTable: 'Table name for INSERT', maxRows: 'Max rows', maxRowsPh: 'No limit',
  filename: 'File name', filenamePh: 'Table name plus time by default', exporting: 'Preparing download…', exportStarted: 'Downloading {file}',
  formats: { csv: 'CSV', tsv: 'TSV', xlsx: 'Excel', json: 'JSON', sql: 'SQL INSERT', markdown: 'Markdown' },
  exportHint: 'The file is streamed by the server, so large tables do not fill memory. The download link works once.',

  dumpTitle: 'Export SQL file · {db}', content: 'Content', both: 'Structure and data', structureOnly: 'Structure only', dataOnly: 'Data only',
  objects: 'Tables and views', selectAll: 'All', selectNone: 'None', selectedN: '{n} of {total} selected', allObjectsHint: 'With everything selected, procedures, functions, events and triggers are exported too. With a subset, only those tables and their triggers are exported.',
  extendedInsert: 'Extended inserts', extendedInsertHelp: 'Combine many rows into one INSERT (Navicat “extended insert”): smaller file, faster import. Off writes one INSERT per row.',
  includeDrop: 'Drop before create', includeDropHelp: 'Write DROP ... IF EXISTS before every CREATE so the import replaces existing objects.',
  createDatabase: 'Include CREATE DATABASE', keepDefiner: 'Keep DEFINER', keepDefinerHelp: 'DEFINER clauses of views, routines, triggers and events are removed by default, so another account can import without SUPER.',
  gzip: 'gzip', noObjects: 'This database has no tables', mustSelect: 'Select at least one table',

  runTitle: 'Run SQL file', file: 'File', chooseFile: 'Choose file', fileTypes: '.sql and .sql.gz files exported by Lemi, Navicat or mysqldump.',
  targetDb: 'Target database', targetDbHelp: 'Statements run in this database when the file has no USE statement.', onError: 'On error', stopOnError: 'Stop and roll back the current batch', continueOnError: 'Continue with the next statements',
  run: 'Run', running: 'Running', cancelRun: 'Abort', statements: '{n} statements executed', errorsN: '{n} errors', currentTable: 'Now: {t}',
  doneOk: 'Finished: {n} statements executed', doneWithErrors: 'Finished: {n} statements, {e} errors', failed: 'Run failed', incomplete: 'The file is incomplete (possibly truncated)',
  sourceFormat: 'Source', formatNames: { lemi: 'Lemi', navicat: 'Navicat', mysqldump: 'mysqldump', unknown: 'Unknown' },
  errLine: 'Line', errIndex: 'Statement', errMsg: 'Error', skipped: '{n} skipped', prodRunTitle: 'Run an SQL file on production?', prodRunHint: '“{name}” is marked as production. Statements in the file change the database directly.',
  aborted: 'Aborted', bytesOf: '{a} / {b}',

  importTitle: 'Import into “{name}”', stepFile: 'File', stepMap: 'Columns', stepDone: 'Done', importFileTypes: 'CSV, TSV (UTF-8) and Excel (.xlsx).',
  sheet: 'Sheet', hasHeader: 'First row has column names', preview: 'Preview (first {n} rows)', mapping: 'Column mapping', tableColumn: 'Table column', fileColumn: 'File column', skipColumn: 'Skip',
  colN: 'Column {n}', autoMap: 'Match by name', emptyAsNull: 'Empty cells as NULL', nullMarker: 'Treat this text as NULL', nullMarkerPh: 'e.g. \\N or NULL',
  errorPolicy: 'On error', rollbackAll: 'Roll back everything', skipBadRows: 'Skip failing rows', doImport: 'Import', importing: 'Importing…',
  imported: '{n} rows imported', importedWithFailed: '{n} rows imported, {f} skipped', importErrors: 'Failed rows', duration: '{ms} ms',
  expired: 'The uploaded file expired, choose it again', noMapping: 'Map at least one column', reupload: 'Choose again', generated: 'generated',

  truncateTitle: 'Truncate table “{name}”?', truncateHint: 'All rows are deleted; the structure stays. This cannot be undone.',
  dropTitle: 'Drop table “{name}”?', dropHint: 'The structure and all rows are deleted. This cannot be undone.',
  typeToConfirm: 'Type the table name “{name}” to confirm:', prodDangerTitle: 'This is production', prodDangerHint: '“{conn}” is marked as production. Run “{sql}” on it?',
  truncated: '{name} truncated', dropped: '{name} dropped',
}
