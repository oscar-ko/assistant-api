package realtime

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// TextScanGate 判斷指定 channel 是否需要對非指令文字訊息做分類掃描。
//
// 這個介面刻意只問 channelID，而不問全域 skill 設定：
// classification 是否需要執行，取決於「此 channel 內是否有人啟用需要文字掃描的 realtime service」。
// skill 的 requires_text_scan 只是能力宣告；真正的啟用狀態必須由 channel_service_members 決定。
type TextScanGate interface {
	HasChannelRealtimeTextScanService(ctx context.Context, channelID uuid.UUID) (bool, error)
}

// ClassificationHandler handles a classified non-command message immediately.
type ClassificationHandler interface {
	HandleClassification(ctx context.Context, messageCtx MessageContext, result ClassificationResult)
}

// MessageClassificationService 對非指令文字訊息做分類，並把分類結果交給即時 handler。
//
// 執行 classifier 前會先通過 TextScanGate：
// - channel 沒有人啟用需要文字掃描的服務時，直接略過，不呼叫模型。
// - 沒有任何 handler 註冊時，也直接略過，避免分類後無人消費結果。
//
// 這讓「是否掃描」回到使用者實際啟用的服務狀態，而不是因為系統有 classifier 設定就掃描所有訊息。
type MessageClassificationService struct {
	textScanGate  TextScanGate
	classifier    Classifier
	handlers      []ClassificationHandler
	platformLabel string
}

// MessageClassificationServiceOptions provides dependencies for message classification.
type MessageClassificationServiceOptions struct {
	TextScanGate  TextScanGate
	Classifier    Classifier
	Handlers      []ClassificationHandler
	PlatformLabel string
}

// NewMessageClassificationService builds a non-command classifier handler.
func NewMessageClassificationService(options MessageClassificationServiceOptions) *MessageClassificationService {
	handlers := make([]ClassificationHandler, 0, len(options.Handlers))
	for _, handler := range options.Handlers {
		if handler == nil {
			continue
		}
		handlers = append(handlers, handler)
	}
	return &MessageClassificationService{
		textScanGate:  options.TextScanGate,
		classifier:    options.Classifier,
		handlers:      handlers,
		platformLabel: strings.TrimSpace(options.PlatformLabel),
	}
}

// Handle 嘗試分類一則非指令文字訊息。
//
// gate 順序刻意由便宜到昂貴：
// 1) 依賴與 handler 是否存在。
// 2) 訊息是否為非空文字且已落庫，確保有 channelID 可查。
// 3) 查詢此 channel 是否有人啟用 requires_text_scan 的 realtime service。
// 4) 只有通過以上條件，才呼叫 classifier。
//
// 這樣可避免最昂貴的分類模型呼叫發生在未啟用服務的 channel 上。
func (s *MessageClassificationService) Handle(ctx context.Context, messageCtx MessageContext) {
	if s == nil || s.textScanGate == nil || s.classifier == nil || len(s.handlers) == 0 {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	message := messageCtx.Message
	if message == nil || !message.IsText() {
		return
	}
	text := strings.TrimSpace(message.Text)
	if text == "" {
		return
	}
	if messageCtx.SavedMessage == nil {
		return
	}

	hasTextScanService, err := s.textScanGate.HasChannelRealtimeTextScanService(ctx, messageCtx.SavedMessage.ChannelID)
	if err != nil {
		zap.L().Warn("realtime classification skipped: query text scan service failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Error(err),
		)
		return
	}
	if !hasTextScanService {
		return
	}

	result, err := s.classifier.Classify(ctx, text)
	if err != nil {
		zap.L().Warn("realtime classification failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Error(err),
		)
		return
	}
	if result == nil || strings.TrimSpace(result.Tag) == "" {
		zap.L().Warn("realtime classification returned empty tag",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		)
		return
	}
	signal := strings.TrimSpace(result.Signal)
	if signal == "" {
		zap.L().Warn("realtime classification returned empty signal",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("tag", strings.TrimSpace(result.Tag)),
		)
		return
	}
	if signal == ClassificationSignalReject {
		zap.L().Info("realtime classification rejected",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("tag", strings.TrimSpace(result.Tag)),
			zap.Float64("confidence", result.Confidence),
			zap.Float64("score_margin", result.ScoreMargin),
		)
		return
	}
	if signal != ClassificationSignalCandidate && signal != ClassificationSignalUnclear {
		zap.L().Warn("realtime classification returned unknown signal",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("tag", strings.TrimSpace(result.Tag)),
			zap.String("signal", signal),
		)
		return
	}

	// tag/signal 是模型訓練與粗篩產物，名稱可能隨訓練資料演進而調整，因此不落庫。
	// 這裡直接 fan-out 給註冊的 handler，讓 todo/calendar 等服務即時消化分類結果。
	for _, handler := range s.handlers {
		handler.HandleClassification(ctx, messageCtx, *result)
	}

	zap.L().Info("realtime classification dispatched",
		zap.String("platform", s.platformLabel),
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("tag", strings.TrimSpace(result.Tag)),
		zap.String("signal", signal),
		zap.Float64("confidence", result.Confidence),
		zap.Float64("score_margin", result.ScoreMargin),
		zap.Int("handler_count", len(s.handlers)),
	)
}
