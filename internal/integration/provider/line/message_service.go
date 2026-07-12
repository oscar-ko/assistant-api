package line

import (
	"context"
	"fmt"
	"strings"

	"assistant-api/internal/config"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"
)

// PushMessageService 定義 LINE 主動推播訊息能力。
type PushMessageService interface {
	PushText(ctx context.Context, lineUserID string, text string) error
}

type pushMessageService struct {
	client *messaging_api.MessagingApiAPI
}

// NewPushMessageService 依設定建立 LINE Messaging API client。
func NewPushMessageService() (PushMessageService, error) {
	token := strings.TrimSpace(config.Line.ChannelToken)
	if token == "" {
		return nil, fmt.Errorf("line channel token is empty")
	}

	client, err := messaging_api.NewMessagingApiAPI(token)
	if err != nil {
		return nil, err
	}

	return &pushMessageService{client: client}, nil
}

// PushText 使用 LINE Push API 送出純文字訊息。
func (s *pushMessageService) PushText(ctx context.Context, lineUserID string, text string) error {
	if s == nil || s.client == nil {
		return fmt.Errorf("line client not initialized")
	}
	if strings.TrimSpace(lineUserID) == "" {
		return fmt.Errorf("line user id is empty")
	}
	if strings.TrimSpace(text) == "" {
		return nil
	}

	req := &messaging_api.PushMessageRequest{
		To: lineUserID,
		Messages: []messaging_api.MessageInterface{
			&messaging_api.TextMessage{Text: text},
		},
	}

	if _, _, err := s.client.WithContext(ctx).PushMessageWithHttpInfo(req, ""); err != nil {
		return err
	}
	return nil
}
