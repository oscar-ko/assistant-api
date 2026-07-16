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
	// SendTextToChat 送出文字訊息。
	// - replyToken 非空：使用 Reply API。
	// - replyToken 為空：使用 Push API。
	// - lineUserID 非空且 chatID != lineUserID：使用 mention 訊息。
	// 參數約定：
	// - chatID: 必填，聊天室/群組/user 對話 ID。
	// - lineUserID: 可空；私聊通常等於 chatID，群組時可指定要 mention 的 user。
	// - text: 必填，空字串會直接略過不送。
	// - replyToken: 可空；有值時會優先用 reply 回覆「當次觸發訊息」。
	// - quoteToken: 可空；有值時會在訊息上附加引用，呈現 quote 某則訊息的 UI。
	// 回傳值：
	// - sentMessageID: LINE 回傳的第一筆 sent message id（若 API 未回傳則為空）。
	SendTextToChat(ctx context.Context, chatID string, lineUserID string, text string, replyToken string, quoteToken string) (string, error)
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

// SendTextToChat 依參數決定使用 Reply 或 Push API，並在群組情境 mention 使用者。
// 設計目的：
// - 用單一入口統一外部呼叫方式，避免呼叫端分散判斷 push/reply。
// - 由 service 內部集中處理輸入正規化、訊息型別組裝與 API 選擇。
func (s *pushMessageService) SendTextToChat(ctx context.Context, chatID string, lineUserID string, text string, replyToken string, quoteToken string) (string, error) {
	if s == nil || s.client == nil {
		return "", fmt.Errorf("line client not initialized")
	}
	chatID = strings.TrimSpace(chatID)
	lineUserID = strings.TrimSpace(lineUserID)
	text = strings.TrimSpace(text)
	replyToken = strings.TrimSpace(replyToken)
	quoteToken = strings.TrimSpace(quoteToken)
	if chatID == "" {
		return "", fmt.Errorf("line chat id is empty")
	}
	if text == "" {
		return "", nil
	}

	// replyToken 有值時代表目前仍在同一 webhook 處理時窗，
	// 優先使用 Reply API 才能真正回覆「使用者剛剛那則訊息」。
	if replyToken != "" {
		req := &messaging_api.ReplyMessageRequest{
			ReplyToken: replyToken,
			Messages:   mentionTextMessages(chatID, lineUserID, text, quoteToken),
		}

		_, model, err := s.client.WithContext(ctx).ReplyMessageWithHttpInfo(req)
		if err != nil {
			return "", err
		}
		if model != nil && len(model.SentMessages) > 0 {
			return strings.TrimSpace(model.SentMessages[0].Id), nil
		}
		return "", nil
	}

	// 無 replyToken 時退回 Push API，可用於追問、補發通知等非即時回覆場景。
	return s.pushMessages(ctx, chatID, mentionTextMessages(chatID, lineUserID, text, quoteToken))
}

// mentionTextMessages 依對話型態回傳最終要送給 LINE 的訊息 payload。
// - 私聊：純文字
// - 群組/聊天室：TextMessageV2 + mention substitution
func mentionTextMessages(chatID string, lineUserID string, text string, quoteToken string) []messaging_api.MessageInterface {
	// private chat 不需要 mention；群組/聊天室則使用 TextMessageV2 mention 使用者。
	if lineUserID == "" || strings.EqualFold(chatID, lineUserID) {
		return []messaging_api.MessageInterface{
			&messaging_api.TextMessage{Text: text, QuoteToken: quoteToken},
		}
	}

	return []messaging_api.MessageInterface{
		&messaging_api.TextMessageV2{
			Text:       "{user} " + text,
			QuoteToken: quoteToken,
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
	}
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

	// Push API 回傳 sentMessages 時只取第一筆 id，
	// 因目前呼叫端一次只送一則訊息，第一筆即為本次追蹤識別。
	_, model, err := s.client.WithContext(ctx).PushMessageWithHttpInfo(req, "")
	if err != nil {
		return "", err
	}
	if model != nil && len(model.SentMessages) > 0 {
		return strings.TrimSpace(model.SentMessages[0].Id), nil
	}
	return "", nil
}
