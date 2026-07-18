package realtime

import (
	"context"
	"strings"

	"go.uber.org/zap"
)

// ClassificationHandler handles a classified non-command message immediately.
type ClassificationHandler interface {
	HandleClassification(ctx context.Context, messageCtx MessageContext, result ClassificationResult)
}

// MessageClassificationService tags non-command text messages and dispatches the result immediately.
type MessageClassificationService struct {
	classifier    Classifier
	handlers      []ClassificationHandler
	platformLabel string
}

// MessageClassificationServiceOptions provides dependencies for message classification.
type MessageClassificationServiceOptions struct {
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
		classifier:    options.Classifier,
		handlers:      handlers,
		platformLabel: strings.TrimSpace(options.PlatformLabel),
	}
}

// Handle classifies and dispatches the tag for a non-command text message.
func (s *MessageClassificationService) Handle(ctx context.Context, messageCtx MessageContext) {
	if s == nil || s.classifier == nil {
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

	// tag 是模型訓練產物，名稱可能隨訓練資料演進而調整，因此不落庫。
	// 這裡直接 fan-out 給註冊的 handler，讓 todo/calendar 等服務即時消化分類結果。
	for _, handler := range s.handlers {
		handler.HandleClassification(ctx, messageCtx, *result)
	}

	zap.L().Info("realtime classification dispatched",
		zap.String("platform", s.platformLabel),
		zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
		zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
		zap.String("tag", strings.TrimSpace(result.Tag)),
		zap.Int("handler_count", len(s.handlers)),
	)
}
