CREATE TABLE IF NOT EXISTS collections (
    name       TEXT PRIMARY KEY,
    note       TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS groups (
    collection TEXT        NOT NULL REFERENCES collections(name) ON DELETE CASCADE,
    name       TEXT        NOT NULL,
    type       TEXT        NOT NULL,
    position   INTEGER     NOT NULL DEFAULT 0,
    config     JSONB       NOT NULL DEFAULT '{}',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (collection, name)
);

-- model_id 刻意不设外键：upstream 目录不在本库，
-- 引用有效性在写入时经下发面校验，运行时以目录快照的 enabled 兜底。
CREATE TABLE IF NOT EXISTS group_members (
    collection TEXT    NOT NULL,
    group_name TEXT    NOT NULL,
    model_id   TEXT    NOT NULL,
    position   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (collection, group_name, model_id),
    FOREIGN KEY (collection, group_name) REFERENCES groups(collection, name) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS group_members_model_idx ON group_members(model_id);

CREATE TABLE IF NOT EXISTS policies (
    name       TEXT PRIMARY KEY,
    language   TEXT        NOT NULL,
    source     TEXT        NOT NULL,
    version    INTEGER     NOT NULL DEFAULT 1,
    note       TEXT        NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS user_models (
    name       TEXT PRIMARY KEY,
    collection TEXT        NOT NULL REFERENCES collections(name),
    policy     TEXT        REFERENCES policies(name),
    client_key TEXT        NOT NULL,
    protocol   TEXT        NOT NULL DEFAULT '',
    enabled    BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS user_models_policy_idx ON user_models(policy);

-- 键为 model_id 而非 (collection, model_id)：目标健康是全局属性，
-- 同一目标在多个 Collection 中共享冷却状态。
CREATE TABLE IF NOT EXISTS target_runtime (
    model_id             TEXT PRIMARY KEY,
    cooling_until        TIMESTAMPTZ,
    consecutive_failures INTEGER     NOT NULL DEFAULT 0,
    input_tokens         BIGINT      NOT NULL DEFAULT 0,
    output_tokens        BIGINT      NOT NULL DEFAULT 0,
    cache_read_tokens    BIGINT      NOT NULL DEFAULT 0,
    request_count        BIGINT      NOT NULL DEFAULT 0,
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS result_reports (
    report_id  TEXT PRIMARY KEY,
    request_id TEXT        NOT NULL,
    model_id   TEXT        NOT NULL,
    outcome    TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS result_reports_created_idx ON result_reports(created_at);
