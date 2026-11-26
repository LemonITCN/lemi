export default {
  run: 'Run', runAll: 'Run all', runHint: 'Run the statement at the cursor or the selection (⌘/Ctrl + Enter)', cancel: 'Cancel', explain: 'Explain', format: 'Format (⌘/Ctrl + Shift + F)',
  noDb: '(no database)', begin: 'Begin', commit: 'Commit', rollback: 'Rollback', inTx: 'In transaction', readOnlyHint: 'You only have read access or the connection is read-only: the session is read-only at the database level',
  placeholder: 'Write SQL here; ⌘/Ctrl + Enter runs the statement at the cursor', result: 'Result', error: 'Error', exec: 'Statement', affected: '{n} rows affected', fetchMore: 'Load more',
  emptyHint: 'Results appear here', txLost: 'The session expired; the open transaction was rolled back', prodDanger: 'Run a dangerous statement on production?', prodDangerHint: '“{name}” is production and the SQL contains DROP, TRUNCATE, or UPDATE/DELETE without WHERE.',
  runAnyway: 'Run anyway', formatFailed: 'Format failed: ', selectInConsole: 'Query in console',
}
