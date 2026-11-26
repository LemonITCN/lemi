-- 健康检查（docs/spec.md §14）：每个连接的检查配置、当前状态、检查记录（保留 30 天）
-- 没有配置行的连接视为已启用、使用默认值（间隔 60 秒、超时 10 秒、连续失败 3 次判定下线）。
CREATE TABLE health_configs (
    connection_id  TEXT PRIMARY KEY REFERENCES connections(id) ON DELETE CASCADE,
    enabled        INTEGER NOT NULL DEFAULT 1,
    interval_sec   INTEGER NOT NULL DEFAULT 60,
    timeout_sec    INTEGER NOT NULL DEFAULT 10,
    fail_threshold INTEGER NOT NULL DEFAULT 3,
    channels       TEXT NOT NULL DEFAULT '[]',     -- 状态变化时通知的渠道 ID（JSON 数组）
    updated_by     TEXT NOT NULL DEFAULT '',
    updated_at     INTEGER NOT NULL
);

CREATE TABLE health_state (
    connection_id   TEXT PRIMARY KEY REFERENCES connections(id) ON DELETE CASCADE,
    status          TEXT NOT NULL DEFAULT 'unknown', -- unknown | up | down
    fails           INTEGER NOT NULL DEFAULT 0,      -- 连续失败次数
    last_check_at   INTEGER NOT NULL DEFAULT 0,
    last_ok_at      INTEGER NOT NULL DEFAULT 0,
    last_latency_ms INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT NOT NULL DEFAULT '',
    changed_at      INTEGER NOT NULL DEFAULT 0       -- 最近一次状态变化的时间
);

CREATE TABLE health_checks (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    connection_id TEXT NOT NULL REFERENCES connections(id) ON DELETE CASCADE,
    at            INTEGER NOT NULL,
    ok            INTEGER NOT NULL,
    latency_ms    INTEGER NOT NULL DEFAULT 0,
    error         TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT '',          -- 本次检查之后的状态
    manual        INTEGER NOT NULL DEFAULT 0         -- 是否为「立即检查」
);
CREATE INDEX health_checks_conn ON health_checks(connection_id, at);
CREATE INDEX health_checks_at ON health_checks(at);
