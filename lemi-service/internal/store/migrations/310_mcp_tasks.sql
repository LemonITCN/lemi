-- MCP：OAuth 2.1 客户端、授权码、令牌，授权（OAuth 与个人访问令牌共用），定时智能体任务
CREATE TABLE oauth_clients (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    redirect_uris TEXT NOT NULL DEFAULT '[]',
    data          TEXT NOT NULL DEFAULT '{}',
    created_at    INTEGER NOT NULL
);

CREATE TABLE oauth_codes (
    id         TEXT PRIMARY KEY,                       -- 授权码的 SHA-256
    client_id  TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    data       TEXT NOT NULL DEFAULT '{}',
    expires_at INTEGER NOT NULL
);

-- 一次授权：OAuth 应用授权或个人访问令牌。选定一个工作空间与每个连接的档位。
CREATE TABLE mcp_grants (
    id           TEXT PRIMARY KEY,
    user_id      TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind         TEXT NOT NULL,                        -- oauth | pat
    client_id    TEXT NOT NULL DEFAULT '',
    name         TEXT NOT NULL DEFAULT '',
    workspace_id TEXT NOT NULL,
    levels       TEXT NOT NULL DEFAULT '{}',           -- 连接 ID → hidden | read | write_auto | full
    token_hash   TEXT NOT NULL DEFAULT '',             -- 个人访问令牌的 SHA-256
    token_hint   TEXT NOT NULL DEFAULT '',             -- 令牌前几位（列表里辨认用）
    expires_at   INTEGER NOT NULL DEFAULT 0,           -- 0 = 不过期
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0,
    revoked_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX mcp_grants_user ON mcp_grants(user_id);
CREATE INDEX mcp_grants_token ON mcp_grants(token_hash);

CREATE TABLE oauth_tokens (
    id         TEXT PRIMARY KEY,                       -- 令牌的 SHA-256
    kind       TEXT NOT NULL,                          -- access | refresh
    client_id  TEXT NOT NULL,
    user_id    TEXT NOT NULL,
    grant_id   TEXT NOT NULL,
    family     TEXT NOT NULL,                          -- 同一次授权衍生出的全部令牌
    code_id    TEXT NOT NULL DEFAULT '',               -- 来自哪个授权码（授权码重放时整条链作废）
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL,
    revoked_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX oauth_tokens_family ON oauth_tokens(family);
CREATE INDEX oauth_tokens_grant ON oauth_tokens(grant_id);
CREATE INDEX oauth_tokens_code ON oauth_tokens(code_id);

CREATE TABLE agent_tasks (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    owner_id     TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE, -- 以谁的身份运行
    name         TEXT NOT NULL,
    schedule     TEXT NOT NULL DEFAULT '{}',          -- sched.Spec
    prompt       TEXT NOT NULL,
    model        TEXT NOT NULL DEFAULT '',
    effort       TEXT NOT NULL DEFAULT '',
    connections  TEXT NOT NULL DEFAULT '{}',          -- 连接 ID → 档位（在任务里再收紧，默认只读）
    channels     TEXT NOT NULL DEFAULT '[]',          -- 通知渠道 ID
    notify_on    TEXT NOT NULL DEFAULT 'always',      -- always | failure | never
    enabled      INTEGER NOT NULL DEFAULT 1,
    next_run_at  INTEGER NOT NULL DEFAULT 0,
    last_run_at  INTEGER NOT NULL DEFAULT 0,
    last_status  TEXT NOT NULL DEFAULT '',
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX agent_tasks_next ON agent_tasks(enabled, next_run_at);

CREATE TABLE agent_task_runs (
    id              TEXT PRIMARY KEY,
    task_id         TEXT NOT NULL REFERENCES agent_tasks(id) ON DELETE CASCADE,
    conversation_id TEXT NOT NULL DEFAULT '',
    trigger         TEXT NOT NULL DEFAULT 'schedule', -- schedule | manual
    status          TEXT NOT NULL,                    -- running | done | error | stopped
    summary         TEXT NOT NULL DEFAULT '',
    report_id       TEXT NOT NULL DEFAULT '',
    error           TEXT NOT NULL DEFAULT '',
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX agent_task_runs_task ON agent_task_runs(task_id, started_at);
