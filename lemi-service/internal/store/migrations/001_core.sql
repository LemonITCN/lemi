-- 主干：用户、会话、登录限速、工作空间、成员、邀请、系统设置、审计
CREATE TABLE users (
    id                  TEXT PRIMARY KEY,
    username            TEXT NOT NULL UNIQUE,
    display_name        TEXT NOT NULL DEFAULT '',
    email               TEXT NOT NULL DEFAULT '',
    role                TEXT NOT NULL DEFAULT 'user',      -- admin | user
    password_hash       TEXT NOT NULL,
    password_changed_at INTEGER NOT NULL DEFAULT 0,
    must_change_password INTEGER NOT NULL DEFAULT 0,
    totp_secret         TEXT NOT NULL DEFAULT '',          -- 密文
    totp_enabled        INTEGER NOT NULL DEFAULT 0,
    totp_last_step      INTEGER NOT NULL DEFAULT 0,        -- 最近一次用过的时间步，防重放
    recovery_codes      TEXT NOT NULL DEFAULT '[]',        -- 恢复码哈希 JSON 数组
    status              TEXT NOT NULL DEFAULT 'active',    -- active | disabled
    locale              TEXT NOT NULL DEFAULT '',
    prefs               TEXT NOT NULL DEFAULT '{}',
    last_login_at       INTEGER NOT NULL DEFAULT 0,
    last_login_ip       TEXT NOT NULL DEFAULT '',
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL
);

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,                          -- 令牌的 SHA-256
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    csrf         TEXT NOT NULL,
    stage        TEXT NOT NULL,                             -- mfa | totp_setup | done
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,
    reauth_at    INTEGER NOT NULL DEFAULT 0,
    ip           TEXT NOT NULL DEFAULT '',
    ua           TEXT NOT NULL DEFAULT ''
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE login_attempts (
    key          TEXT PRIMARY KEY,                          -- u:<用户名> | ip:<地址> | mfa:<用户 ID>
    failures     INTEGER NOT NULL DEFAULT 0,
    locks        INTEGER NOT NULL DEFAULT 0,                -- 已锁过几次（锁定时长翻倍）
    first_at     INTEGER NOT NULL,
    last_at      INTEGER NOT NULL,
    locked_until INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE known_devices (
    user_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    ip          TEXT NOT NULL DEFAULT '',
    ua          TEXT NOT NULL DEFAULT '',
    first_at    INTEGER NOT NULL,
    last_at     INTEGER NOT NULL,
    PRIMARY KEY (user_id, fingerprint)
);

CREATE TABLE workspaces (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    settings    TEXT NOT NULL DEFAULT '{}',
    created_by  TEXT NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE workspace_members (
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role         TEXT NOT NULL,                             -- viewer | member | admin | owner
    created_at   INTEGER NOT NULL,
    PRIMARY KEY (workspace_id, user_id)
);
CREATE INDEX workspace_members_user ON workspace_members(user_id);

CREATE TABLE invites (
    id             TEXT PRIMARY KEY,                        -- 令牌的 SHA-256
    created_by     TEXT NOT NULL,
    role           TEXT NOT NULL DEFAULT 'user',
    workspace_id   TEXT NOT NULL DEFAULT '',
    workspace_role TEXT NOT NULL DEFAULT '',
    note           TEXT NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    expires_at     INTEGER NOT NULL,
    used_at        INTEGER NOT NULL DEFAULT 0,
    used_by        TEXT NOT NULL DEFAULT ''
);

CREATE TABLE settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE audit_log (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    at            INTEGER NOT NULL,
    user_id       TEXT NOT NULL DEFAULT '',
    username      TEXT NOT NULL DEFAULT '',
    workspace_id  TEXT NOT NULL DEFAULT '',
    connection_id TEXT NOT NULL DEFAULT '',
    source        TEXT NOT NULL DEFAULT '',                 -- ui | grid | agent | mcp | task | system | cli
    action        TEXT NOT NULL,
    target        TEXT NOT NULL DEFAULT '',
    detail        TEXT NOT NULL DEFAULT '{}',
    ip            TEXT NOT NULL DEFAULT '',
    ok            INTEGER NOT NULL DEFAULT 1,
    error         TEXT NOT NULL DEFAULT '',
    duration_ms   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX audit_at ON audit_log(at);
CREATE INDEX audit_user ON audit_log(user_id, at);
CREATE INDEX audit_ws ON audit_log(workspace_id, at);
CREATE INDEX audit_conn ON audit_log(connection_id, at);
