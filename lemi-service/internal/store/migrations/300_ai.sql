-- 智能体：模型供应商、会话、消息、运行记录、结果集、图表与图、报告
CREATE TABLE ai_providers (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL,                      -- opencode-go | custom
    name        TEXT NOT NULL,
    base_url    TEXT NOT NULL DEFAULT '',
    api_key     TEXT NOT NULL DEFAULT '',           -- 密文
    protocol    TEXT NOT NULL DEFAULT '',           -- chat-completions | messages | responses（自定义供应商）
    models      TEXT NOT NULL DEFAULT '[]',         -- 手填的模型 JSON
    auto_models INTEGER NOT NULL DEFAULT 0,         -- 从 /models 自动拉取
    enabled     INTEGER NOT NULL DEFAULT 1,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);

CREATE TABLE agent_conversations (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind          TEXT NOT NULL DEFAULT 'chat',     -- chat | task | sub（子智能体）
    task_id       TEXT NOT NULL DEFAULT '',         -- 定时任务的运行来自哪个任务
    parent_id     TEXT NOT NULL DEFAULT '',         -- 子智能体所属的会话
    title         TEXT NOT NULL DEFAULT '',
    settings      TEXT NOT NULL DEFAULT '{}',       -- 模型、推理档位、模式
    state         TEXT NOT NULL DEFAULT '{}',       -- 摘要、上下文实测、允许规则、计划清单、环境指纹
    usage         TEXT NOT NULL DEFAULT '{}',
    system_prompt TEXT NOT NULL DEFAULT '',         -- 会话开始时固定的系统提示
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL,
    archived_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX agent_conversations_user ON agent_conversations(user_id, workspace_id, updated_at);

CREATE TABLE agent_messages (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES agent_conversations(id) ON DELETE CASCADE,
    seq             INTEGER NOT NULL,
    role            TEXT NOT NULL,                   -- user | assistant | tool
    parts           TEXT NOT NULL DEFAULT '[]',
    meta            TEXT NOT NULL DEFAULT '{}',
    created_at      INTEGER NOT NULL,
    UNIQUE (conversation_id, seq)
);

CREATE TABLE agent_runs (
    id              TEXT PRIMARY KEY,
    conversation_id TEXT NOT NULL REFERENCES agent_conversations(id) ON DELETE CASCADE,
    user_id         TEXT NOT NULL,
    status          TEXT NOT NULL,                   -- running | waiting | done | error | stopped
    error           TEXT NOT NULL DEFAULT '',
    steps           INTEGER NOT NULL DEFAULT 0,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX agent_runs_conv ON agent_runs(conversation_id, started_at);
CREATE INDEX agent_runs_status ON agent_runs(status);

CREATE TABLE ai_results (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    user_id       TEXT NOT NULL,
    kind          TEXT NOT NULL,                     -- rows | text
    connection_id TEXT NOT NULL DEFAULT '',
    database      TEXT NOT NULL DEFAULT '',
    sql           TEXT NOT NULL DEFAULT '',
    title         TEXT NOT NULL DEFAULT '',
    columns       TEXT NOT NULL DEFAULT '[]',
    row_count     INTEGER NOT NULL DEFAULT 0,
    truncated     INTEGER NOT NULL DEFAULT 0,        -- 取满上限，后面还有
    bytes         INTEGER NOT NULL DEFAULT 0,
    source        TEXT NOT NULL DEFAULT '',          -- agent | mcp | task
    created_at    INTEGER NOT NULL
);
CREATE INDEX ai_results_created ON ai_results(created_at);

CREATE TABLE ai_charts (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id       TEXT NOT NULL,
    kind          TEXT NOT NULL,                     -- echarts | mermaid
    title         TEXT NOT NULL DEFAULT '',
    spec          TEXT NOT NULL DEFAULT '{}',        -- echarts：清洗后的 option 模板与 encode；mermaid：代码
    source        TEXT NOT NULL DEFAULT '{}',        -- 数据来源：连接、库、SQL、结果 ID（刷新用）
    data          TEXT NOT NULL DEFAULT '{}',        -- echarts：dataset 快照
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE INDEX ai_charts_ws ON ai_charts(workspace_id, created_at);

CREATE TABLE reports (
    id               TEXT PRIMARY KEY,
    workspace_id     TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by       TEXT NOT NULL,
    updated_by       TEXT NOT NULL DEFAULT '',
    title            TEXT NOT NULL,
    body             TEXT NOT NULL DEFAULT '',
    snapshot         TEXT NOT NULL DEFAULT '{}',     -- 引用块的数据快照
    conversation_id  TEXT NOT NULL DEFAULT '',
    task_id          TEXT NOT NULL DEFAULT '',
    share_token      TEXT NOT NULL DEFAULT '',       -- 公开链接令牌的哈希
    share_password   TEXT NOT NULL DEFAULT '',       -- 访问密码的 argon2 哈希
    share_expires_at INTEGER NOT NULL DEFAULT 0,
    share_created_at INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);
CREATE INDEX reports_ws ON reports(workspace_id, updated_at);
CREATE INDEX reports_share ON reports(share_token);
