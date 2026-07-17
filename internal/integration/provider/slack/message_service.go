package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"assistant-api/internal/config"
)

// PushMessageService 定義 Slack 外送訊息能力。
type PushMessageService interface {
	SendTextToChat(ctx context.Context, chatID string, userID string, text string, replyRef string, quoteRef string) (string, error)
}

type pushMessageService struct {
	token      string
	httpClient *http.Client
}

// NewPushMessageService 依設定建立 Slack chat.postMessage client。
func NewPushMessageService() (PushMessageService, error) {
	token := strings.TrimSpace(config.Slack.BotToken)
	if token == "" {
		return nil, fmt.Errorf("slack bot token is empty")
	}
	return &pushMessageService{
		token:      token,
		httpClient: &http.Client{Timeout: 15 * time.Second},
	}, nil
}

type slackPostMessageRequest struct {
	Channel  string `json:"channel"`
	Text     string `json:"text"`
	ThreadTS string `json:"thread_ts,omitempty"`
}

type slackPostMessageResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	TS      string `json:"ts"`
	Message struct {
		TS string `json:"ts"`
	} `json:"message"`
}

func (s *pushMessageService) SendTextToChat(ctx context.Context, chatID string, userID string, text string, replyRef string, quoteRef string) (string, error) {
	if s == nil || s.httpClient == nil {
		return "", fmt.Errorf("slack message client not initialized")
	}
	chatID = strings.TrimSpace(chatID)
	userID = strings.TrimSpace(userID)
	text = strings.TrimSpace(text)
	replyRef = strings.TrimSpace(replyRef)
	if chatID == "" {
		return "", fmt.Errorf("slack channel id is empty")
	}
	if text == "" {
		return "", nil
	}

	outboundText := text
	if userID != "" && !strings.EqualFold(chatID, userID) {
		mentionPrefix := "<@" + userID + ">"
		if !strings.Contains(outboundText, mentionPrefix) {
			outboundText = mentionPrefix + " " + outboundText
		}
	}

	payload := slackPostMessageRequest{
		Channel:  chatID,
		Text:     outboundText,
		ThreadTS: replyRef,
	}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/chat.postMessage", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("slack post message failed: status=%d", resp.StatusCode)
	}

	var result slackPostMessageResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", err
	}
	if !result.OK {
		if strings.TrimSpace(result.Error) == "" {
			return "", fmt.Errorf("slack post message failed")
		}
		return "", fmt.Errorf("slack post message failed: %s", result.Error)
	}
	if ts := strings.TrimSpace(result.TS); ts != "" {
		return ts, nil
	}
	return strings.TrimSpace(result.Message.TS), nil
}
