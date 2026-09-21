-- Existing installations must run this migration once.
ALTER TABLE request_log
    ADD COLUMN IF NOT EXISTS first_packet_latency_ms int NOT NULL DEFAULT 0;

COMMENT ON COLUMN request_log.first_packet_latency_ms IS '收到模型首个响应包耗时(毫秒)，0表示未采集';
