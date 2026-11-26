-- 连接、成员收紧、查询历史、收藏 SQL、上传的 SQLite 文件
CREATE TABLE connections (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    driver        TEXT NOT NULL,
    params        TEXT NOT NULL DEFAULT '{}',   -- 驱动参数 JSON，密钥字段为密文
    network       TEXT NOT NULL DEFAULT '{}',   -- SSH / 代理 JSON，密钥字段为密文
    color         TEXT NOT NULL DEFAULT '',
    folder        TEXT NOT NULL DEFAULT '',
    env           TEXT NOT NULL DEFAULT '',     -- prod | test | dev | ''
    read_only     INTEGER NOT NULL DEFAULT 0,   -- 连接级强制只读（所有人）
    ai_level      TEXT NOT NULL DEFAULT 'read', -- hidden | read | write_confirm | write_auto | full
    ai_allow      TEXT NOT NULL DEFAULT '[]',   -- AI 库表白名单（库.表 通配）
    notes         TEXT NOT NULL DEFAULT '',
    sort          INTEGER NOT NULL DEFAULT 0,
    created_by    TEXT NOT NULL DEFAULT '',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE INDEX connections_ws ON connections(workspace_id);

CREATE TABLE connection_limits (
    connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    level         TEXT NOT NULL,                -- none | read | write
    PRIMARY KEY (connection_id, user_id)
);

CREATE TABLE query_history (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id       TEXT NOT NULL,
    connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    database      TEXT NOT NULL DEFAULT '',
    sql           TEXT NOT NULL,
    source        TEXT NOT NULL DEFAULT 'ui',
    at            INTEGER NOT NULL,
    duration_ms   INTEGER NOT NULL DEFAULT 0,
    ok            INTEGER NOT NULL DEFAULT 1,
    rows          INTEGER NOT NULL DEFAULT 0,
    error         TEXT NOT NULL DEFAULT ''
);
CREATE INDEX query_history_user ON query_history(user_id, connection_id, at);

CREATE TABLE saved_queries (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    connection_id TEXT NOT NULL DEFAULT '',
    folder        TEXT NOT NULL DEFAULT '',
    name          TEXT NOT NULL,
    sql           TEXT NOT NULL,
    shared        INTEGER NOT NULL DEFAULT 0,
    created_by    TEXT NOT NULL,
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE INDEX saved_queries_ws ON saved_queries(workspace_id);

CREATE TABLE uploaded_sqlite (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    size         INTEGER NOT NULL DEFAULT 0,
    created_by   TEXT NOT NULL,
    created_at   INTEGER NOT NULL
);
