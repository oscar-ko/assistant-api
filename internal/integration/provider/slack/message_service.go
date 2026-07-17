package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"assistant-api/internal/config"
)

func slackBotTokenStrict() (string, error) {
	token := strings.TrimSpace(config.Slack.BotToken)
	if token == "" {
		return "", fmt.Errorf("slack bot token is empty")
	}
	if !strings.HasPrefix(token, "xoxb-") {
		return "", fmt.Errorf("slack bot token is invalid format (expected xoxb-)")
	}
	return token, nil
}

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

	// 群組場景下若提供 userID，會補 mention 前綴，
	// 讓 bot 回覆更容易被指定對象注意到；
	// 私訊場景（chatID == userID）則不強制 mention。
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
	// 部分回應可能把 ts 放在 message.ts，這裡兼容該格式。
	return strings.TrimSpace(result.Message.TS), nil
}

type slackOpenDMRequest struct {
	Users string `json:"users"`
}

type slackOpenDMResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
}

// OpenDMChannelID opens a Slack DM with the target user and returns DM channel id.
//
// 嚴格模式：缺 token、缺 user id、或 Slack API 回錯都直接回傳錯誤。
func OpenDMChannelID(ctx context.Context, userID string) (string, error) {
	token, err := slackBotTokenStrict()
	if err != nil {
		return "", err
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", fmt.Errorf("slack user id is empty")
	}

	// conversations.open 支援以 users 傳入目標 user，
	// Slack 會回傳既有 DM 或新建立 DM 的 channel 物件。
	payload := slackOpenDMRequest{Users: userID}
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://slack.com/api/conversations.open", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")

	httpClient := &http.Client{Timeout: 15 * time.Second}
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("slack open dm failed: status=%d", resp.StatusCode)
	}

	var result slackOpenDMResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return "", err
	}
	if !result.OK {
		if strings.TrimSpace(result.Error) == "" {
			return "", fmt.Errorf("slack open dm failed")
		}
		return "", fmt.Errorf("slack open dm failed: %s", result.Error)
	}

	dmChannelID := strings.TrimSpace(result.Channel.ID)
	if dmChannelID == "" {
		// API 成功但沒給 channel.id 視為不完整回應，直接中止。
		return "", fmt.Errorf("slack open dm failed: empty channel id")
	}
	return dmChannelID, nil
}

type slackUsersInfoResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	User  struct {
		Profile struct {
			DisplayName string `json:"display_name"`
			RealName    string `json:"real_name"`
		} `json:"profile"`
		Name string `json:"name"`
	} `json:"user"`
}

// GetUserDisplayNameByID resolves Slack user's display name by user ID.
func GetUserDisplayNameByID(ctx context.Context, userID string) (string, error) {
	token, err := slackBotTokenStrict()
	if err != nil {
		return "", err
	}
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return "", fmt.Errorf("slack user id is empty")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://slack.com/api/users.info?user="+url.QueryEscape(userID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("slack users.info failed: status=%d", resp.StatusCode)
	}

	var parsed slackUsersInfoResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if !parsed.OK {
		if strings.TrimSpace(parsed.Error) == "" {
			return "", fmt.Errorf("slack users.info failed")
		}
		errCode := strings.TrimSpace(parsed.Error)
		if strings.EqualFold(errCode, "invalid_auth") {
			return "", fmt.Errorf("slack users.info failed: invalid_auth (check active config slack.bot_token)")
		}
		return "", fmt.Errorf("slack users.info failed: %s", errCode)
	}

	if v := strings.TrimSpace(parsed.User.Profile.DisplayName); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(parsed.User.Profile.RealName); v != "" {
		return v, nil
	}
	if v := strings.TrimSpace(parsed.User.Name); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("slack user display name is empty")
}

type slackConversationInfoResponse struct {
	OK      bool   `json:"ok"`
	Error   string `json:"error"`
	Channel struct {
		Name string `json:"name"`
	} `json:"channel"`
}

// GetChannelNameByID resolves Slack conversation name by channel ID.
func GetChannelNameByID(ctx context.Context, channelID string) (string, error) {
	token, err := slackBotTokenStrict()
	if err != nil {
		return "", err
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return "", fmt.Errorf("slack channel id is empty")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://slack.com/api/conversations.info?channel="+url.QueryEscape(channelID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("slack conversations.info failed: status=%d", resp.StatusCode)
	}

	var parsed slackConversationInfoResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if !parsed.OK {
		if strings.TrimSpace(parsed.Error) == "" {
			return "", fmt.Errorf("slack conversations.info failed")
		}
		return "", fmt.Errorf("slack conversations.info failed: %s", strings.TrimSpace(parsed.Error))
	}

	name := strings.TrimSpace(parsed.Channel.Name)
	if name == "" {
		return "", fmt.Errorf("slack channel name is empty")
	}
	return name, nil
}
