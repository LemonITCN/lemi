-- 备份：存储目标、计划、运行记录、每个目标上的文件、还原记录、空间级备份设置（docs/spec.md §12）
CREATE TABLE backup_targets (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL,                  -- local | sftp | webdav | ftp | smb | s3 | cos | oss | azure | baidu
    config       TEXT NOT NULL DEFAULT '{}',     -- JSON，密钥字段为密文
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX backup_targets_ws ON backup_targets(workspace_id);

CREATE TABLE backup_plans (
    id                TEXT PRIMARY KEY,
    workspace_id      TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    connection_id     TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    name              TEXT NOT NULL,
    enabled           INTEGER NOT NULL DEFAULT 1,
    method            TEXT NOT NULL,              -- mysqldump | lemi | sqlite_file
    databases         TEXT NOT NULL DEFAULT '[]', -- MySQL 选的库，空 = 全部用户库
    compression       TEXT NOT NULL DEFAULT 'zstd', -- zstd | gzip | none
    compression_level INTEGER NOT NULL DEFAULT 5,
    encrypt           INTEGER NOT NULL DEFAULT 0,
    passphrase        TEXT NOT NULL DEFAULT '',   -- 备份密码（密文）
    targets           TEXT NOT NULL DEFAULT '[]', -- 存储目标 ID
    schedule          TEXT NOT NULL DEFAULT '{"kind":"manual"}',
    retention         TEXT NOT NULL DEFAULT '{}',
    verify            TEXT NOT NULL DEFAULT '{}', -- {"after_backup":bool,"schedule":计划}
    notify_success    TEXT NOT NULL DEFAULT '[]',
    notify_failure    TEXT NOT NULL DEFAULT '[]',
    next_run_at       INTEGER NOT NULL DEFAULT 0,
    retry_at          INTEGER NOT NULL DEFAULT 0, -- 失败后自动重试的时间
    retry_attempt     INTEGER NOT NULL DEFAULT 0, -- 已经重试了几次
    next_verify_at    INTEGER NOT NULL DEFAULT 0,
    created_by        TEXT NOT NULL DEFAULT '',
    created_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL
);
CREATE INDEX backup_plans_ws ON backup_plans(workspace_id);
CREATE INDEX backup_plans_conn ON backup_plans(connection_id);

CREATE TABLE backup_runs (
    id              TEXT PRIMARY KEY,
    workspace_id    TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    plan_id         TEXT NOT NULL DEFAULT '',
    plan_name       TEXT NOT NULL DEFAULT '',
    connection_id   TEXT NOT NULL,                -- 连接删掉后记录与文件保留
    connection_name TEXT NOT NULL DEFAULT '',
    driver          TEXT NOT NULL DEFAULT '',
    trigger_kind    TEXT NOT NULL,                -- schedule | manual | retry
    attempt         INTEGER NOT NULL DEFAULT 0,
    status          TEXT NOT NULL,                -- running | success | failed | canceled
    method          TEXT NOT NULL DEFAULT '',
    tool            TEXT NOT NULL DEFAULT '',     -- mysqldump 8.4.13.1 | lemi-sqldump/1 | sqlite-vacuum-into
    server_version  TEXT NOT NULL DEFAULT '',
    databases       TEXT NOT NULL DEFAULT '[]',
    file_name       TEXT NOT NULL DEFAULT '',
    compression     TEXT NOT NULL DEFAULT '',
    encrypted       INTEGER NOT NULL DEFAULT 0,
    passphrase      TEXT NOT NULL DEFAULT '',     -- 本次备份用的密码（密文，计划改密码后旧备份仍能还原）
    size            INTEGER NOT NULL DEFAULT 0,   -- 写到目标上的字节数
    raw_size        INTEGER NOT NULL DEFAULT 0,   -- 压缩加密前的字节数
    sha256          TEXT NOT NULL DEFAULT '',
    manifest        TEXT NOT NULL DEFAULT '{}',
    warnings        TEXT NOT NULL DEFAULT '[]',
    error           TEXT NOT NULL DEFAULT '',
    created_by      TEXT NOT NULL DEFAULT '',
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER NOT NULL DEFAULT 0,
    verify_status   TEXT NOT NULL DEFAULT '',     -- '' | running | ok | failed
    verify_at       INTEGER NOT NULL DEFAULT 0,
    verify_detail   TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX backup_runs_ws ON backup_runs(workspace_id, id);
CREATE INDEX backup_runs_plan ON backup_runs(plan_id, started_at);
CREATE INDEX backup_runs_conn ON backup_runs(connection_id, started_at);
CREATE INDEX backup_runs_status ON backup_runs(status);

CREATE TABLE backup_files (
    run_id    TEXT NOT NULL REFERENCES backup_runs(id) ON DELETE CASCADE,
    target_id TEXT NOT NULL,
    path      TEXT NOT NULL,                      -- 相对目标根目录
    size      INTEGER NOT NULL DEFAULT 0,
    status    TEXT NOT NULL,                      -- ok | failed
    error     TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (run_id, target_id)
);
CREATE INDEX backup_files_target ON backup_files(target_id);

CREATE TABLE backup_restores (
    id                   TEXT PRIMARY KEY,
    workspace_id         TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    run_id               TEXT NOT NULL,
    target_id            TEXT NOT NULL DEFAULT '',  -- 从哪个存储目标读取
    connection_id        TEXT NOT NULL,             -- 还原到的连接
    connection_name      TEXT NOT NULL DEFAULT '',
    database_map         TEXT NOT NULL DEFAULT '{}',
    method               TEXT NOT NULL DEFAULT '',  -- mysql | lemi | sqlite_file
    status               TEXT NOT NULL,             -- running | success | failed | canceled
    error                TEXT NOT NULL DEFAULT '',
    result               TEXT NOT NULL DEFAULT '{}',
    created_by           TEXT NOT NULL DEFAULT '',
    started_at           INTEGER NOT NULL,
    finished_at          INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX backup_restores_ws ON backup_restores(workspace_id, id);

CREATE TABLE backup_settings (
    workspace_id         TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
    verify_connection_id TEXT NOT NULL DEFAULT '',  -- 还原校验用的 MySQL 连接
    updated_at           INTEGER NOT NULL
);
