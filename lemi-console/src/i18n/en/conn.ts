export default {
  newTitle: 'New connection', editTitle: 'Edit connection', chooseType: 'Choose a database type', moreSoon: 'PostgreSQL, MongoDB, Elasticsearch and more will arrive as driver plugins.',
  tabGeneral: 'General', tabSSH: 'SSH tunnel', tabProxy: 'Proxy', test: 'Test', port: 'Port', env: 'Environment', envHelp: 'Production connections are marked red and writes get extra warnings.',
  folder: 'Folder', color: 'Color', secretKept: 'Set — leave empty to keep',
  sshEnable: 'Connect through an SSH tunnel', sshHelp: 'Connect to a jump host first, which then connects to the database. The host fingerprint must be confirmed on first use.', sshHost: 'SSH host', sshAuth: 'Authentication', sshKey: 'Private key',
  sshPassphrase: 'Key passphrase', sshHostKey: 'Trusted host fingerprint', sshHostKeyHelp: 'Once pinned, a changed fingerprint (possible man-in-the-middle or reinstall) blocks the connection.', sshHostKeyNone: 'Not confirmed yet (confirm when testing)',
  hostKeyFirst: 'First connection to this SSH host. Verify the fingerprint with the server administrator before trusting it.', hostKeyMismatch: 'The SSH host fingerprint changed! This may be a man-in-the-middle attack or a reinstalled host. Only trust it if you are sure.', trustHostKey: 'Trust this fingerprint',
  proxyType: 'Proxy type', proxyHelp: 'Connect through a SOCKS5 or HTTP CONNECT proxy (with SSH, the proxy is used to reach the SSH host).', proxyNone: 'No proxy', proxyHost: 'Proxy host',
  aiIntro: 'What the AI agent may do on this connection. Read-only is enforced by the database session, not by guessing SQL. The user’s own permission still applies.',
  ai: {
    hidden: 'Hidden', hiddenDesc: 'The AI cannot see this connection', read: 'Read only', readDesc: 'Only reads schema and data',
    writeConfirm: 'Read/write · confirm each', writeConfirmDesc: 'Every write needs your confirmation', writeAuto: 'Read/write · DML auto', writeAutoDesc: 'INSERT/UPDATE/DELETE run directly; DDL still needs confirmation',
    full: 'Full access', fullDesc: 'Everything runs directly (needs instance admin approval)',
  },
  aiAllow: 'Allowed tables (optional)', aiAllowHelp: 'One “db.table” per line, * wildcards allowed, e.g. shop.order_*. Empty = no limit. Objects outside the list are invisible to the AI.',
  readOnly: 'Read-only connection', readOnlyHelp: 'Everyone (admins included) can only read on this connection, enforced by the database session.', notes: 'Notes',
  sqliteUploaded: 'Uploaded files', sqliteServer: 'Server folders', sqliteUpload: 'Upload SQLite file', sqliteNoUploads: 'No uploaded files yet',
  sqliteNoDirs: 'The instance admin has not configured server folders (LEMI_SQLITE_DIRS).',
}
