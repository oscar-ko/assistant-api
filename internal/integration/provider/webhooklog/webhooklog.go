package webhooklog

import (
	"strings"

	"go.uber.org/zap"
)

// IncomingMessage 代表 webhook 進入點的共用 log 資料。
type IncomingMessage struct {
	Provider      string
	EventType     string
	SourceType    string
	SourceUserID  string
	SourceGroupID string
	SourceRoomID  string
	MessageID     string
	Text          string
}

// UnifiedConversionSkipped 代表訊息無法轉成 unified message 的 log 資料。
type UnifiedConversionSkipped struct {
	Provider      string
	EventType     string
	SourceType    string
	SourceUserID  string
	SourceGroupID string
	SourceRoomID  string
	MessageID     string
	Reason        string
}

// LogIncomingMessage 印出 webhook 收到的原始訊息摘要。
func LogIncomingMessage(log IncomingMessage) {
	zap.L().Info(strings.TrimSpace(log.Provider)+" message received",
		zap.String("event_type", strings.TrimSpace(log.EventType)),
		zap.String("source_type", strings.TrimSpace(log.SourceType)),
		zap.String("source_user_id", strings.TrimSpace(log.SourceUserID)),
		zap.String("source_group_id", strings.TrimSpace(log.SourceGroupID)),
		zap.String("source_room_id", strings.TrimSpace(log.SourceRoomID)),
		zap.String("message_id", strings.TrimSpace(log.MessageID)),
		zap.String("text", strings.TrimSpace(log.Text)),
	)
}

// LogUnifiedConversionSkipped 印出訊息無法轉成 unified message 的原因。
func LogUnifiedConversionSkipped(log UnifiedConversionSkipped) {
	zap.L().Debug(strings.TrimSpace(log.Provider)+" message unified conversion skipped",
		zap.String("event_type", strings.TrimSpace(log.EventType)),
		zap.String("source_type", strings.TrimSpace(log.SourceType)),
		zap.String("source_user_id", strings.TrimSpace(log.SourceUserID)),
		zap.String("source_group_id", strings.TrimSpace(log.SourceGroupID)),
		zap.String("source_room_id", strings.TrimSpace(log.SourceRoomID)),
		zap.String("message_id", strings.TrimSpace(log.MessageID)),
		zap.String("reason", strings.TrimSpace(log.Reason)),
	)
}
