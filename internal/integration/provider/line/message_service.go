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
	PushMentionTextToChat(ctx context.Context, chatID string, lineUserID string, text string) (string, error)
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

	_, err := s.pushMessages(ctx, lineUserID, []messaging_api.MessageInterface{
		&messaging_api.TextMessage{Text: text},
	})
	if err != nil {
		return err
	}
	return nil
}

// PushMentionTextToChat 使用 LINE Push API 在同聊天室送出 @user 追問訊息。
// 若 chatID 與 lineUserID 相同（通常代表 private chat），則退化為一般文字訊息。
func (s *pushMessageService) PushMentionTextToChat(ctx context.Context, chatID string, lineUserID string, text string) (string, error) {
	if s == nil || s.client == nil {
		return "", fmt.Errorf("line client not initialized")
	}
	chatID = strings.TrimSpace(chatID)
	lineUserID = strings.TrimSpace(lineUserID)
	text = strings.TrimSpace(text)
	if chatID == "" {
		return "", fmt.Errorf("line chat id is empty")
	}
	if text == "" {
		return "", nil
	}

	// private chat 不需要 mention；群組/聊天室則使用 TextMessageV2 mention 使用者。
	if lineUserID == "" || strings.EqualFold(chatID, lineUserID) {
		return s.pushMessages(ctx, chatID, []messaging_api.MessageInterface{
			&messaging_api.TextMessage{Text: text},
		})
	}

	return s.pushMessages(ctx, chatID, []messaging_api.MessageInterface{
		&messaging_api.TextMessageV2{
			Text: "{user} " + text,
			Substitution: map[string]messaging_api.SubstitutionObjectInterface{
				"user": &messaging_api.MentionSubstitutionObject{
					SubstitutionObject: messaging_api.SubstitutionObject{Type: "mention"},
					Mentionee: &messaging_api.UserMentionTarget{
						MentionTarget: messaging_api.MentionTarget{Type: "user"},
						UserId:        lineUserID,
					},
				},
			},
		},
	})
}

func (s *pushMessageService) pushMessages(ctx context.Context, to string, messages []messaging_api.MessageInterface) (string, error) {
	if s == nil || s.client == nil {
		return "", fmt.Errorf("line client not initialized")
	}
	to = strings.TrimSpace(to)
	if to == "" {
		return "", fmt.Errorf("line receiver id is empty")
	}
	if len(messages) == 0 {
		return "", nil
	}

	req := &messaging_api.PushMessageRequest{
		To:       to,
		Messages: messages,
	}

	_, model, err := s.client.WithContext(ctx).PushMessageWithHttpInfo(req, "")
	if err != nil {
		return "", err
	}
	if model != nil && len(model.SentMessages) > 0 {
		return strings.TrimSpace(model.SentMessages[0].Id), nil
	}
	return "", nil
}
