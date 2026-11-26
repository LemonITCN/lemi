-- 通知（docs/spec.md §15）：工作空间的通知渠道、发送记录（保留 30 天）
-- 实例级「系统邮件服务器」存在 settings 表的 notify.smtp 键里（口令为密文）。
CREATE TABLE notify_channels (
    id           TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name         TEXT NOT NULL,
    kind         TEXT NOT NULL,                  -- email | webhook | dingtalk | wecom | feishu
    config       TEXT NOT NULL DEFAULT '{}',     -- 配置 JSON，密钥字段为密文
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_by   TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
);
CREATE INDEX notify_channels_ws ON notify_channels(workspace_id);

-- 每次发送（一个事件发到一个渠道）一行；重试只更新 attempts。系统邮件（安全事件）的 workspace_id 为 NULL。
CREATE TABLE notify_logs (
    id           TEXT PRIMARY KEY,                -- ULID，时间有序，用作分页游标
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE,
    channel_id   TEXT NOT NULL DEFAULT '',        -- 渠道删除后保留记录，不加外键
    channel_name TEXT NOT NULL DEFAULT '',
    kind         TEXT NOT NULL,
    event        TEXT NOT NULL,
    level        TEXT NOT NULL DEFAULT 'info',
    title        TEXT NOT NULL DEFAULT '',
    message      TEXT NOT NULL DEFAULT '',
    status       TEXT NOT NULL DEFAULT 'sending', -- sending | ok | failed
    attempts     INTEGER NOT NULL DEFAULT 0,
    error        TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    finished_at  INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX notify_logs_ws ON notify_logs(workspace_id, id);
CREATE INDEX notify_logs_channel ON notify_logs(channel_id, id);
CREATE INDEX notify_logs_at ON notify_logs(created_at);
CREATE INDEX notify_logs_status ON notify_logs(status);
