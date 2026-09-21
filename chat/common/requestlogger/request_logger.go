// Package logger 请求日志记录工具
package logger

import (
	"context"
	"time"

	"chat/service/chat/model"

	"github.com/google/uuid"
	"github.com/zeromicro/go-zero/core/logx"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// RequestLogger 请求日志记录器
type RequestLogger struct {
	db *gorm.DB
}

// NewRequestLogger 创建请求日志记录器
func NewRequestLogger(db *gorm.DB) *RequestLogger {
	return &RequestLogger{db: db}
}

// LogRequest 记录请求日志（新增或更新）
func (l *RequestLogger) LogRequest(ctx context.Context, req *model.RequestLog) {
	if req.RequestID == "" {
		req.RequestID = uuid.New().String()
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	if req.UpdatedAt.IsZero() {
		req.UpdatedAt = time.Now()
	}

	// 使用 upsert：如果 request_id 已存在则更新，否则新增
	result := l.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "request_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"request_type", "res_content", "res_content_length", "status", "error_msg", "latency_ms", "first_packet_latency_ms", "prompt_tokens", "completion_tokens", "total_tokens", "conversation_id", "transfer_id", "updated_at"}),
		}).
		Create(req)
	if result.Error != nil {
		logx.Errorf("[RequestLogger] LogRequest failed: %v, request_id: %s", result.Error, req.RequestID)
	}
}

// LogTransfer 记录转人工日志（新增或更新）
func (l *RequestLogger) LogTransfer(ctx context.Context, req *model.TransferLog) {
	if req.TransferID == "" {
		req.TransferID = uuid.New().String()
	}
	if req.CreatedAt.IsZero() {
		req.CreatedAt = time.Now()
	}
	if req.UpdatedAt.IsZero() {
		req.UpdatedAt = time.Now()
	}

	// 使用 upsert：如果 transfer_id 已存在则更新，否则新增
	result := l.db.WithContext(ctx).
		Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "transfer_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"service_state", "assigned_servicer_id", "assignment_type", "was_in_working_hours", "transfer_status", "accepted_at", "wait_duration_ms", "session_end_at", "session_duration_ms", "transfer_success", "error_msg", "webhook_notified", "updated_at"}),
		}).
		Create(req)
	if result.Error != nil {
		logx.Errorf("[RequestLogger] LogTransfer failed: %v, transfer_id: %s", result.Error, req.TransferID)
	}
}

// LogRequestAndTransfer 同时记录请求和转人工日志（在同一事务中）
func (l *RequestLogger) LogRequestAndTransfer(ctx context.Context, reqLog *model.RequestLog, transferLog *model.TransferLog) {
	if reqLog.RequestID == "" {
		reqLog.RequestID = uuid.New().String()
	}
	if reqLog.CreatedAt.IsZero() {
		reqLog.CreatedAt = time.Now()
	}

	if transferLog.TransferID == "" {
		transferLog.TransferID = uuid.New().String()
	}
	transferLog.RequestID = reqLog.RequestID
	transferLog.UpdatedAt = time.Now()

	if err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(reqLog).Error; err != nil {
			return err
		}
		return tx.Create(transferLog).Error
	}); err != nil {
		logx.Errorf("[RequestLogger] LogRequestAndTransfer failed: %v, request_id: %s, transfer_id: %s",
			err, reqLog.RequestID, transferLog.TransferID)
	}
}
