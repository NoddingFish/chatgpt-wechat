package types

// ConsultationStatsReq 查询咨询统计。StartDate/EndDate 与 Period 二选一：
// 1. 指定 start_date、end_date（YYYY-MM-DD）；
// 2. period=week/month，可选 reference_date（默认当天）。
type ConsultationStatsReq struct {
	Period        string `form:"period,optional" json:"period,optional"`
	ReferenceDate string `form:"reference_date,optional" json:"reference_date,optional"`
	StartDate     string `form:"start_date,optional" json:"start_date,optional"`
	EndDate       string `form:"end_date,optional" json:"end_date,optional"`
}

type ConsultationStatsReply struct {
	AIConsultationTotal       int64    `json:"ai_consultation_total"`
	HumanTransferTotal        int64    `json:"human_transfer_total"`
	TransferUserCount         int64    `json:"transfer_user_count"`
	DirectHumanUserCount      int64    `json:"direct_human_user_count"`
	HumanConsultationUsers    int64    `json:"human_consultation_users"`
	UniqueLogUsers            int64    `json:"unique_log_users"`
	UniqueSessions            int64    `json:"unique_sessions"`
	DirectHumanUserIDs        []string `json:"direct_human_user_ids"`
	StartDate                 string   `json:"start_date"`
	EndDate                   string   `json:"end_date"`
	AverageLatencyMs          float64  `json:"average_latency_ms"`
	AverageFirstPacketLatency float64  `json:"average_first_packet_latency_ms"`
	AveragePromptTokens       float64  `json:"average_prompt_tokens"`
	AverageCompletionTokens   float64  `json:"average_completion_tokens"`
	TokenSampleCount          int64    `json:"token_sample_count"`
	FirstPacketSampleCount    int64    `json:"first_packet_sample_count"`
	Report                    string   `json:"report"`
}
