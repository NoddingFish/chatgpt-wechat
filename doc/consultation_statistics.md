# 咨询统计

## 数据口径

- **智能客服咨询总数**：`request_log.request_type = 'ai_chat'` 的请求数，包含成功、失败和处理中请求。
- **人工咨询发起数**：统计期内 `transfer_log` 的记录数；包含转接成功、失败、取消等所有发起行为。
- **转人工用户数**：统计期内 `transfer_log.user_id` 去重数。
- **直接发起人工用户**：统计期内抓取到 `human_chat` 人工消息、但系统中不存在该用户转人工记录的用户。这表示用户可能绕过智能客服直接进入人工。
- **咨询人工用户数**：转人工用户数 + 直接发起人工用户数（两组互斥）。
- **日志独立 user_id**：统计期内 `request_log.user_id` 去重数（包括 AI、指令、转人工和人工消息日志）。
- **独立会话数**：仅统计 AI 咨询；同一用户相邻咨询间隔超过 30 分钟即视为新会话。采用该规则是因为旧数据中的 `conversation_id` 并不完整。
- **平均响应延迟**：成功 AI 请求从开始处理至获得完整回答的平均耗时。
- **平均首包延迟**：成功 AI 请求从开始处理至收到首个有效回答片段的平均耗时。仅新增字段上线后的流式 Coze 请求有该数据。
- **平均输入/输出 token**：仅对成功且 Coze 返回 usage 的 AI 请求计算。响应同时返回样本数，避免把没有 token 数据的请求按 0 计入平均值。

统计时区固定为 `Asia/Shanghai`，内部使用 `[开始日 00:00, 结束日后一天 00:00)`，不会漏掉结束日数据。

## API

接口需要与其他管理 API 相同的 JWT：

```http
GET /api/stats/consultations?period=week&reference_date=2026-09-09
Authorization: Bearer <token>
```

自然周按周一至周日计算。自然月示例：

```http
GET /api/stats/consultations?period=month&reference_date=2026-09-09
```

自定义闭区间：

```http
GET /api/stats/consultations?start_date=2026-09-06&end_date=2026-09-12
```

未提供任何参数时返回本周。自定义范围最长 366 天。

## 旧数据库升级

新建数据库会由 `build/pgvector/init/request_log.sql` 创建完整字段。已运行的数据库必须手工执行一次：

```bash
psql "$DATABASE_URL" -f build/pgvector/migrations/20260921_add_request_log_first_packet_latency.sql
```

Docker 示例：

```bash
docker exec -i pgvector psql -U chat -d chat < build/pgvector/migrations/20260921_add_request_log_first_packet_latency.sql
```

迁移前启动新版本会因为缺少 `first_packet_latency_ms` 导致请求日志写入失败，因此应先执行迁移再发布应用。
