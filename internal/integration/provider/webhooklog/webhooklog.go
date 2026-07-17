package webhooklog

import (
	"strings"

	"go.uber.org/zap"
)

// IncomingMessage 代表 webhook 進入點的共用 log 資料。
//
// 這個結構只負責承接「原始事件長什麼樣子」的最小資訊，
// 讓 LINE / Slack 兩邊都可以用同一個 log helper 印出進入點摘要，
// 不需要各自維護一套 logger 欄位組裝邏輯。
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
//
// 當 provider 事件因為格式不完整、類型不支援或訊息內容不足而無法轉成
// unifiedmessage.Message 時，就用這個結構把原因統一記錄下來，方便 LINE / Slack
// 共用同一種除錯格式。
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
//
// 所有欄位都在這裡集中 TrimSpace，是為了避免呼叫端重複做相同清理，
// 也讓各平台的 log 呈現保持一致。
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
//
// 這裡只記錄「為什麼沒轉成功」，不負責做任何補償或 fallback，
// 目的是讓 webhook 進入點的行為保持可預測，方便直接對照原始事件。
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
