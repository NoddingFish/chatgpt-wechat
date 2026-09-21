package logic

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"chat/service/chat/api/internal/svc"
	"chat/service/chat/api/internal/types"
)

const consultationSessionGap = 30 * time.Minute

type ConsultationStatsLogic struct {
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewConsultationStatsLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ConsultationStatsLogic {
	return &ConsultationStatsLogic{ctx: ctx, svcCtx: svcCtx}
}

type consultationAggregate struct {
	AIConsultationTotal       int64   `gorm:"column:ai_consultation_total"`
	UniqueLogUsers            int64   `gorm:"column:unique_log_users"`
	AverageLatencyMs          float64 `gorm:"column:average_latency_ms"`
	AverageFirstPacketLatency float64 `gorm:"column:average_first_packet_latency_ms"`
	AveragePromptTokens       float64 `gorm:"column:average_prompt_tokens"`
	AverageCompletionTokens   float64 `gorm:"column:average_completion_tokens"`
	TokenSampleCount          int64   `gorm:"column:token_sample_count"`
	FirstPacketSampleCount    int64   `gorm:"column:first_packet_sample_count"`
}

type transferAggregate struct {
	HumanTransferTotal int64 `gorm:"column:human_transfer_total"`
	TransferUserCount  int64 `gorm:"column:transfer_user_count"`
}

// ConsultationStats 返回按自然周（周一至周日）、自然月或自定义日期范围统计的数据。
func (l *ConsultationStatsLogic) ConsultationStats(req *types.ConsultationStatsReq) (*types.ConsultationStatsReply, error) {
	// 报表包含外部联系人 ID，只允许管理员访问。
	userID, ok := l.ctx.Value("userId").(json.Number)
	if !ok {
		return nil, fmt.Errorf("unauthorized")
	}
	userIDValue, err := userID.Int64()
	if err != nil {
		return nil, fmt.Errorf("unauthorized")
	}
	admin, err := l.svcCtx.UserModel.User.WithContext(l.ctx).
		Where(l.svcCtx.UserModel.User.ID.Eq(userIDValue)).First()
	if err != nil || !admin.IsAdmin {
		return nil, fmt.Errorf("administrator permission required")
	}

	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return nil, fmt.Errorf("load statistics timezone: %w", err)
	}
	start, endExclusive, err := consultationDateRange(req, time.Now().In(location), location)
	if err != nil {
		return nil, err
	}

	var ai consultationAggregate
	if err := l.svcCtx.DbEngin.WithContext(l.ctx).Raw(`
SELECT
    COUNT(*) FILTER (WHERE request_type = 'ai_chat') AS ai_consultation_total,
    COUNT(DISTINCT user_id) AS unique_log_users,
    COALESCE(AVG(latency_ms) FILTER (WHERE request_type = 'ai_chat' AND status = 'success'), 0) AS average_latency_ms,
    COALESCE(AVG(first_packet_latency_ms) FILTER (WHERE request_type = 'ai_chat' AND status = 'success' AND first_packet_latency_ms > 0), 0) AS average_first_packet_latency_ms,
    COALESCE(AVG(prompt_tokens) FILTER (WHERE request_type = 'ai_chat' AND status = 'success' AND total_tokens > 0), 0) AS average_prompt_tokens,
    COALESCE(AVG(completion_tokens) FILTER (WHERE request_type = 'ai_chat' AND status = 'success' AND total_tokens > 0), 0) AS average_completion_tokens,
    COUNT(*) FILTER (WHERE request_type = 'ai_chat' AND status = 'success' AND total_tokens > 0) AS token_sample_count,
    COUNT(*) FILTER (WHERE request_type = 'ai_chat' AND status = 'success' AND first_packet_latency_ms > 0) AS first_packet_sample_count
FROM request_log
WHERE created_at >= ? AND created_at < ?`, start, endExclusive).Scan(&ai).Error; err != nil {
		return nil, fmt.Errorf("query consultation aggregate: %w", err)
	}

	var transfer transferAggregate
	if err := l.svcCtx.DbEngin.WithContext(l.ctx).Raw(`
SELECT COUNT(*) AS human_transfer_total, COUNT(DISTINCT user_id) AS transfer_user_count
FROM transfer_log
WHERE created_at >= ? AND created_at < ?`, start, endExclusive).Scan(&transfer).Error; err != nil {
		return nil, fmt.Errorf("query transfer aggregate: %w", err)
	}

	var uniqueSessions int64
	if err := l.svcCtx.DbEngin.WithContext(l.ctx).Raw(`
WITH ordered AS (
    SELECT user_id, created_at,
           LAG(created_at) OVER (PARTITION BY user_id ORDER BY created_at) AS previous_at
    FROM request_log
    WHERE request_type = 'ai_chat' AND created_at >= ? AND created_at < ?
)
SELECT COALESCE(SUM(CASE WHEN previous_at IS NULL OR created_at - previous_at > (? * INTERVAL '1 second') THEN 1 ELSE 0 END), 0) AS unique_sessions
FROM ordered`, start, endExclusive, int64(consultationSessionGap/time.Second)).Scan(&uniqueSessions).Error; err != nil {
		return nil, fmt.Errorf("query unique sessions: %w", err)
	}

	// “直接发起人工”指已抓取到人工客服消息，但没有任何 transfer_log 的用户。
	// 这类会话通常由用户从企业微信入口直接请求人工，而非由本系统执行转接。
	var directHumanUserIDs []string
	if err := l.svcCtx.DbEngin.WithContext(l.ctx).Raw(`
SELECT DISTINCT r.user_id
FROM request_log r
WHERE r.request_type = 'human_chat'
  AND r.created_at >= ? AND r.created_at < ?
  AND NOT EXISTS (
      SELECT 1 FROM transfer_log t
      WHERE t.user_id = r.user_id AND t.created_at >= ? AND t.created_at < ?
  )
ORDER BY r.user_id`, start, endExclusive, start, endExclusive).Scan(&directHumanUserIDs).Error; err != nil {
		return nil, fmt.Errorf("query direct human users: %w", err)
	}

	reply := &types.ConsultationStatsReply{
		AIConsultationTotal:       ai.AIConsultationTotal,
		HumanTransferTotal:        transfer.HumanTransferTotal,
		TransferUserCount:         transfer.TransferUserCount,
		DirectHumanUserCount:      int64(len(directHumanUserIDs)),
		HumanConsultationUsers:    transfer.TransferUserCount + int64(len(directHumanUserIDs)),
		UniqueLogUsers:            ai.UniqueLogUsers,
		UniqueSessions:            uniqueSessions,
		DirectHumanUserIDs:        directHumanUserIDs,
		StartDate:                 start.Format(time.DateOnly),
		EndDate:                   endExclusive.AddDate(0, 0, -1).Format(time.DateOnly),
		AverageLatencyMs:          roundOne(ai.AverageLatencyMs),
		AverageFirstPacketLatency: roundOne(ai.AverageFirstPacketLatency),
		AveragePromptTokens:       roundOne(ai.AveragePromptTokens),
		AverageCompletionTokens:   roundOne(ai.AverageCompletionTokens),
		TokenSampleCount:          ai.TokenSampleCount,
		FirstPacketSampleCount:    ai.FirstPacketSampleCount,
	}
	reply.Report = formatConsultationReport(reply)
	return reply, nil
}

func formatConsultationReport(stats *types.ConsultationStatsReply) string {
	directUsers := "无"
	if len(stats.DirectHumanUserIDs) > 0 {
		directUsers = strings.Join(stats.DirectHumanUserIDs, "、")
	}
	return fmt.Sprintf(`智能客服咨询总数：%d
人工咨询发起数：%d（%d个用户 + %d个用户直接发起人工 = %d个用户）
咨询人工用户数：%d
================================================================
日志独立 user_id：%d    独立会话数：%d
直接发起人工（日志无记录）用户：%s
日期范围：%s ~ %s
平均响应延迟：%.1f ms    平均首包延迟：%.1f ms
平均输入 token：%.1f    平均输出 token：%.1f`,
		stats.AIConsultationTotal,
		stats.HumanTransferTotal,
		stats.TransferUserCount,
		stats.DirectHumanUserCount,
		stats.HumanConsultationUsers,
		stats.HumanConsultationUsers,
		stats.UniqueLogUsers,
		stats.UniqueSessions,
		directUsers,
		stats.StartDate,
		stats.EndDate,
		stats.AverageLatencyMs,
		stats.AverageFirstPacketLatency,
		stats.AveragePromptTokens,
		stats.AverageCompletionTokens,
	)
}

func consultationDateRange(req *types.ConsultationStatsReq, now time.Time, location *time.Location) (time.Time, time.Time, error) {
	if req.StartDate != "" || req.EndDate != "" {
		if req.StartDate == "" || req.EndDate == "" {
			return time.Time{}, time.Time{}, fmt.Errorf("start_date and end_date must be provided together")
		}
		start, err := time.ParseInLocation(time.DateOnly, req.StartDate, location)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid start_date, expected YYYY-MM-DD")
		}
		end, err := time.ParseInLocation(time.DateOnly, req.EndDate, location)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid end_date, expected YYYY-MM-DD")
		}
		if end.Before(start) {
			return time.Time{}, time.Time{}, fmt.Errorf("end_date must not be before start_date")
		}
		if end.Sub(start) > 366*24*time.Hour {
			return time.Time{}, time.Time{}, fmt.Errorf("date range must not exceed 366 days")
		}
		return start, end.AddDate(0, 0, 1), nil
	}

	reference := now
	if req.ReferenceDate != "" {
		parsed, err := time.ParseInLocation(time.DateOnly, req.ReferenceDate, location)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid reference_date, expected YYYY-MM-DD")
		}
		reference = parsed
	}
	reference = time.Date(reference.Year(), reference.Month(), reference.Day(), 0, 0, 0, 0, location)

	switch strings.ToLower(req.Period) {
	case "", "week":
		weekdayOffset := (int(reference.Weekday()) + 6) % 7 // Monday = 0
		start := reference.AddDate(0, 0, -weekdayOffset)
		return start, start.AddDate(0, 0, 7), nil
	case "month":
		start := time.Date(reference.Year(), reference.Month(), 1, 0, 0, 0, 0, location)
		return start, start.AddDate(0, 1, 0), nil
	default:
		return time.Time{}, time.Time{}, fmt.Errorf("period must be week or month")
	}
}

func roundOne(value float64) float64 {
	return math.Round(value*10) / 10
}
