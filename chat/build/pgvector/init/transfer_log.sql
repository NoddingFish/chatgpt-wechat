CREATE TABLE IF NOT EXISTS transfer_log
(
    id                     bigserial PRIMARY KEY,
    transfer_id            varchar(64)  NOT NULL,
    user_id                varchar(191) NOT NULL,
    user_type              varchar(20)  NOT NULL DEFAULT 'customer',
    agent_id               bigint       NOT NULL DEFAULT 0,
    open_kf_id             varchar(191) NOT NULL DEFAULT '',
    request_id             varchar(64)  NOT NULL DEFAULT '',
    transfer_reason        varchar(200) NOT NULL DEFAULT '',
    user_message           text         NOT NULL,
    service_state          int          NOT NULL DEFAULT 2,
    assigned_servicer_id   varchar(100) NOT NULL DEFAULT '',
    assignment_type        varchar(20)  NOT NULL DEFAULT 'round_robin',
    was_in_working_hours   boolean      NOT NULL DEFAULT true,
    transfer_status        varchar(20)  NOT NULL DEFAULT 'pending',
    accepted_at           timestamp,
    wait_duration_ms       int          NOT NULL DEFAULT 0,
    session_end_at         timestamp,
    session_duration_ms    int          NOT NULL DEFAULT 0,
    transfer_success       boolean      NOT NULL DEFAULT false,
    error_msg              text,
    webhook_notified       boolean      NOT NULL DEFAULT false,
    created_at             timestamp    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at             timestamp    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE transfer_log IS '转人工记录表';
COMMENT ON COLUMN transfer_log.id IS '主键ID';
COMMENT ON COLUMN transfer_log.transfer_id IS '转人工唯一标识(UUID)';
COMMENT ON COLUMN transfer_log.user_id IS '用户ID';
COMMENT ON COLUMN transfer_log.user_type IS '用户类型: wecom/customer';
COMMENT ON COLUMN transfer_log.agent_id IS '应用ID';
COMMENT ON COLUMN transfer_log.open_kf_id IS '客服ID';
COMMENT ON COLUMN transfer_log.request_id IS '关联请求ID';
COMMENT ON COLUMN transfer_log.transfer_reason IS '转人工原因';
COMMENT ON COLUMN transfer_log.user_message IS '用户触发转人工的消息内容';
COMMENT ON COLUMN transfer_log.service_state IS '服务状态: 0-未处理 1-AI接待 2-排队中 3-人工接待';
COMMENT ON COLUMN transfer_log.assigned_servicer_id IS '分配的接待人员ID';
COMMENT ON COLUMN transfer_log.assignment_type IS '分配方式: round_robin/direct/fallback';
COMMENT ON COLUMN transfer_log.was_in_working_hours IS '是否在工作时间内';
COMMENT ON COLUMN transfer_log.transfer_status IS '转人工状态: pending/accepted/timeout/cancelled';
COMMENT ON COLUMN transfer_log.accepted_at IS '客服接受时间';
COMMENT ON COLUMN transfer_log.wait_duration_ms IS '排队等待时长(毫秒)';
COMMENT ON COLUMN transfer_log.session_end_at IS '会话结束时间';
COMMENT ON COLUMN transfer_log.session_duration_ms IS '会话总时长(毫秒)';
COMMENT ON COLUMN transfer_log.transfer_success IS '转人工是否成功';
COMMENT ON COLUMN transfer_log.error_msg IS '错误信息';
COMMENT ON COLUMN transfer_log.webhook_notified IS '是否已发送webhook通知';
COMMENT ON COLUMN transfer_log.created_at IS '创建时间';
COMMENT ON COLUMN transfer_log.updated_at IS '更新时间';

CREATE UNIQUE INDEX IF NOT EXISTS transfer_log_transfer_id_idx ON transfer_log (transfer_id);
CREATE INDEX IF NOT EXISTS transfer_log_user_id_idx ON transfer_log (user_id);
CREATE INDEX IF NOT EXISTS transfer_log_agent_id_idx ON transfer_log (agent_id);
CREATE INDEX IF NOT EXISTS transfer_log_open_kf_id_idx ON transfer_log (open_kf_id);
CREATE INDEX IF NOT EXISTS transfer_log_request_id_idx ON transfer_log (request_id);
CREATE INDEX IF NOT EXISTS transfer_log_transfer_status_idx ON transfer_log (transfer_status);
CREATE INDEX IF NOT EXISTS transfer_log_created_at_idx ON transfer_log (created_at);
CREATE INDEX IF NOT EXISTS transfer_log_user_created_idx ON transfer_log (user_id, created_at);
CREATE INDEX IF NOT EXISTS transfer_log_kf_created_idx ON transfer_log (open_kf_id, created_at);
