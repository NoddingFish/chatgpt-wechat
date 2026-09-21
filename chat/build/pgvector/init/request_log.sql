CREATE TABLE IF NOT EXISTS request_log
(
    id                  bigserial PRIMARY KEY,
    request_id          varchar(64)  NOT NULL,
    request_type        varchar(20)  NOT NULL DEFAULT 'ai_chat',
    user_id             varchar(191) NOT NULL,
    user_type           varchar(20)  NOT NULL DEFAULT 'customer',
    agent_id            bigint       NOT NULL DEFAULT 0,
    channel             varchar(50)  NOT NULL DEFAULT '',
    model_name          varchar(100) NOT NULL DEFAULT '',
    req_content         text         NOT NULL,
    req_content_length  int          NOT NULL DEFAULT 0,
    is_voice            boolean      NOT NULL DEFAULT false,
    res_content         text         NOT NULL,
    res_content_length  int          NOT NULL DEFAULT 0,
    status              varchar(20)  NOT NULL DEFAULT 'success',
    error_msg           text,
    latency_ms          int          NOT NULL DEFAULT 0,
    prompt_tokens       int          NOT NULL DEFAULT 0,
    completion_tokens   int          NOT NULL DEFAULT 0,
    total_tokens        int          NOT NULL DEFAULT 0,
    conversation_id     varchar(191) NOT NULL DEFAULT '',
    transfer_id         varchar(64)  NOT NULL DEFAULT '',
    created_at          timestamp    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at          timestamp    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE request_log IS '请求记录表';
COMMENT ON COLUMN request_log.id IS '主键ID';
COMMENT ON COLUMN request_log.request_id IS '请求唯一标识(UUID)';
COMMENT ON COLUMN request_log.request_type IS '请求类型: ai_chat/transfer_op/command_op/human_chat';
COMMENT ON COLUMN request_log.user_id IS '用户ID';
COMMENT ON COLUMN request_log.user_type IS '用户类型: wecom/customer';
COMMENT ON COLUMN request_log.agent_id IS '应用ID';
COMMENT ON COLUMN request_log.channel IS 'AI渠道';
COMMENT ON COLUMN request_log.model_name IS '模型名称';
COMMENT ON COLUMN request_log.req_content IS '用户发送内容';
COMMENT ON COLUMN request_log.req_content_length IS '请求内容长度';
COMMENT ON COLUMN request_log.is_voice IS '是否为语音请求';
COMMENT ON COLUMN request_log.res_content IS 'AI响应内容';
COMMENT ON COLUMN request_log.res_content_length IS '响应内容长度';
COMMENT ON COLUMN request_log.status IS '处理状态: success/failed/timeout';
COMMENT ON COLUMN request_log.error_msg IS '错误信息';
COMMENT ON COLUMN request_log.latency_ms IS '响应耗时(毫秒)';
COMMENT ON COLUMN request_log.prompt_tokens IS '消耗prompt token数';
COMMENT ON COLUMN request_log.completion_tokens IS '消耗completion token数';
COMMENT ON COLUMN request_log.total_tokens IS '总消耗token数';
COMMENT ON COLUMN request_log.conversation_id IS '会话ID';
COMMENT ON COLUMN request_log.transfer_id IS '关联转人工ID';
COMMENT ON COLUMN request_log.created_at IS '创建时间';
COMMENT ON COLUMN request_log.updated_at IS '更新时间';

CREATE UNIQUE INDEX IF NOT EXISTS request_log_request_id_idx ON request_log (request_id);
CREATE INDEX IF NOT EXISTS request_log_request_type_idx ON request_log (request_type);
CREATE INDEX IF NOT EXISTS request_log_user_id_idx ON request_log (user_id);
CREATE INDEX IF NOT EXISTS request_log_agent_id_idx ON request_log (agent_id);
CREATE INDEX IF NOT EXISTS request_log_channel_idx ON request_log (channel);
CREATE INDEX IF NOT EXISTS request_log_status_idx ON request_log (status);
CREATE INDEX IF NOT EXISTS request_log_created_at_idx ON request_log (created_at);
CREATE INDEX IF NOT EXISTS request_log_user_created_idx ON request_log (user_id, created_at);
CREATE INDEX IF NOT EXISTS request_log_agent_created_idx ON request_log (agent_id, created_at);
CREATE INDEX IF NOT EXISTS request_log_type_created_idx ON request_log (request_type, created_at);
