package graph

import (
	"context"
	"crypto/hmac"
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"strings"

	"assistant-api/internal/ent/slack"

	"github.com/google/uuid"
)

type devSimulationParticipant struct {
	UserID         uuid.UUID
	PlatformUserID string
	Name           string
}

// 以下 simulated* 結構只描述 dev helper 要送進 provider webhook processor 的最小平台 payload。
// 它們刻意維持接近平台原始 webhook JSON 的欄位命名，讓模擬資料能穿過正式 adapter，
// 而不是在 GraphQL resolver 裡直接組 unifiedmessage.Message 或 ChannelMessage。

type simulatedLineWebhookBody struct {
	Events []simulatedLineWebhookEvent `json:"events"`
}

type simulatedLineWebhookEvent struct {
	Type      string                      `json:"type"`
	Source    simulatedLineWebhookSource  `json:"source"`
	Message   simulatedLineWebhookMessage `json:"message"`
	Timestamp int64                       `json:"timestamp"`
}

type simulatedLineWebhookSource struct {
	Type    string `json:"type"`
	UserID  string `json:"userId"`
	GroupID string `json:"groupId"`
}

type simulatedLineWebhookMessage struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

type simulatedSlackWebhookBody struct {
	Type   string                `json:"type"`
	TeamID string                `json:"team_id"`
	Event  simulatedSlackMessage `json:"event"`
}

type simulatedSlackMessage struct {
	Type        string `json:"type"`
	User        string `json:"user"`
	Text        string `json:"text"`
	Channel     string `json:"channel"`
	ChannelType string `json:"channel_type"`
	TS          string `json:"ts"`
	ClientMsgID string `json:"client_msg_id"`
}

func (r *Resolver) pickRandomLineParticipants(ctx context.Context, count int) ([]devSimulationParticipant, error) {
	lines, err := r.Client.Line.Query().WithUser().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query line users failed: %w", err)
	}
	if len(lines) < count {
		return nil, fmt.Errorf("not enough line-bound users: need %d, got %d", count, len(lines))
	}

	participants := make([]devSimulationParticipant, 0, len(lines))
	for _, item := range lines {
		if item == nil || strings.TrimSpace(item.PlatformUserID) == "" {
			continue
		}
		user, err := item.Edges.UserOrErr()
		if err != nil {
			return nil, fmt.Errorf("line user edge is not loaded: %w", err)
		}
		name := strings.TrimSpace(user.Name)
		if item.DisplayName != nil && strings.TrimSpace(*item.DisplayName) != "" {
			name = strings.TrimSpace(*item.DisplayName)
		}
		if name == "" {
			return nil, fmt.Errorf("line-bound user display name is empty: %s", user.ID.String())
		}
		participants = append(participants, devSimulationParticipant{UserID: user.ID, PlatformUserID: strings.TrimSpace(item.PlatformUserID), Name: name})
	}
	if len(participants) < count {
		return nil, fmt.Errorf("not enough usable line-bound users: need %d, got %d", count, len(participants))
	}
	if err := shuffleParticipants(participants); err != nil {
		return nil, err
	}
	return participants[:count], nil
}

func (r *Resolver) pickRandomSlackParticipants(ctx context.Context, teamID string, count int) ([]devSimulationParticipant, error) {
	teamID = strings.TrimSpace(teamID)
	if teamID == "" {
		return nil, fmt.Errorf("slack platformTenantID is required")
	}
	// Slack user id 只在單一 workspace 內有意義，所以挑測試參與者時必須用 teamID 限縮。
	// 如果跨 workspace 混抽使用者，後續 ResolveBoundUserIDByPlatformIdentity 會找不到正確的系統 user。
	slacks, err := r.Client.Slack.Query().Where(slack.PlatformTeamIDEQ(teamID)).WithUser().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query slack users failed: %w", err)
	}
	if len(slacks) < count {
		return nil, fmt.Errorf("not enough slack-bound users for team %s: need %d, got %d", teamID, count, len(slacks))
	}

	participants := make([]devSimulationParticipant, 0, len(slacks))
	for _, item := range slacks {
		if item == nil || strings.TrimSpace(item.PlatformUserID) == "" {
			continue
		}
		user, err := item.Edges.UserOrErr()
		if err != nil {
			return nil, fmt.Errorf("slack user edge is not loaded: %w", err)
		}
		name := strings.TrimSpace(user.Name)
		if item.DisplayName != nil && strings.TrimSpace(*item.DisplayName) != "" {
			name = strings.TrimSpace(*item.DisplayName)
		}
		if name == "" {
			return nil, fmt.Errorf("slack-bound user display name is empty: %s", user.ID.String())
		}
		participants = append(participants, devSimulationParticipant{UserID: user.ID, PlatformUserID: strings.TrimSpace(item.PlatformUserID), Name: name})
	}
	if len(participants) < count {
		return nil, fmt.Errorf("not enough usable slack-bound users for team %s: need %d, got %d", teamID, count, len(participants))
	}
	if err := shuffleParticipants(participants); err != nil {
		return nil, err
	}
	return participants[:count], nil
}

func (r *Resolver) pickRandomSimulationParticipants(ctx context.Context, platform string, platformTenantID string, count int) ([]devSimulationParticipant, error) {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "line":
		return r.pickRandomLineParticipants(ctx, count)
	case "slack":
		return r.pickRandomSlackParticipants(ctx, platformTenantID, count)
	default:
		return nil, fmt.Errorf("unsupported simulation platform: %s", platform)
	}
}

func (r *Resolver) buildSimulatedWebhookBody(platform string, platformTenantID string, platformChannelID string, participant devSimulationParticipant, text string, timestamp int64) (string, []byte, error) {
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "line":
		platformMessageID := "dev-line-todo-" + uuid.NewString()
		body, err := marshalSimulatedLineWebhook(participant, platformChannelID, platformMessageID, text, timestamp)
		if err != nil {
			return "", nil, fmt.Errorf("marshal simulated line webhook failed: %w", err)
		}
		return platformMessageID, body, nil
	case "slack":
		// Slack 的 message ts 同時是平台訊息 ID，也是 thread/reply 參照用的穩定值。
		// 使用 Slack 常見的 seconds.microseconds 形狀，讓 dev 資料更接近真實 Events API payload。
		platformMessageID := fmt.Sprintf("%d.%06d", timestamp/1000, timestamp%1000*1000)
		body, err := marshalSimulatedSlackWebhook(platformTenantID, participant, platformChannelID, "channel", platformMessageID, text)
		if err != nil {
			return "", nil, fmt.Errorf("marshal simulated slack webhook failed: %w", err)
		}
		return platformMessageID, body, nil
	default:
		return "", nil, fmt.Errorf("unsupported simulation platform: %s", platform)
	}
}

func buildPlatformSimulationText(platform string, displayName string, text string) string {
	displayName = strings.TrimSpace(displayName)
	text = strings.TrimSpace(text)
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "line":
		if displayName == "" {
			return text
		}
		return displayName + "：" + text
	case "slack":
		if displayName == "" {
			return text
		}
		return displayName + ": " + text
	default:
		return text
	}
}

func marshalSimulatedLineWebhook(participant devSimulationParticipant, platformChannelID string, platformMessageID string, text string, timestamp int64) ([]byte, error) {
	return json.Marshal(simulatedLineWebhookBody{Events: []simulatedLineWebhookEvent{{
		Type: "message",
		Source: simulatedLineWebhookSource{
			Type:    "group",
			UserID:  participant.PlatformUserID,
			GroupID: platformChannelID,
		},
		Message: simulatedLineWebhookMessage{
			ID:   platformMessageID,
			Type: "text",
			Text: text,
		},
		Timestamp: timestamp,
	}}})
}

func marshalSimulatedSlackWebhook(teamID string, participant devSimulationParticipant, platformChannelID string, channelType string, platformMessageID string, text string) ([]byte, error) {
	return json.Marshal(simulatedSlackWebhookBody{
		Type:   "event_callback",
		TeamID: strings.TrimSpace(teamID),
		Event: simulatedSlackMessage{
			Type:        "message",
			User:        participant.PlatformUserID,
			Text:        text,
			Channel:     platformChannelID,
			ChannelType: channelType,
			TS:          platformMessageID,
			ClientMsgID: "dev-slack-todo-" + uuid.NewString(),
		},
	})
}

func signSimulatedSlackWebhook(signingSecret string, timestamp string, body []byte) string {
	// 正式 Slack webhook 入口會驗證 X-Slack-Signature，並用命中的 signing secret 決定本次 app_id。
	// dev helper 若直接跳過簽章，會漏測多 bot 情境最關鍵的 app selection；因此這裡依 Slack v0 規格產生簽章。
	base := "v0:" + strings.TrimSpace(timestamp) + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(strings.TrimSpace(signingSecret)))
	_, _ = mac.Write([]byte(base))
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

func trimOptionalString(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func shuffleParticipants(items []devSimulationParticipant) error {
	for index := len(items) - 1; index > 0; index-- {
		randomIndex, err := cryptoRandomInt(index + 1)
		if err != nil {
			return fmt.Errorf("randomize participants failed: %w", err)
		}
		items[index], items[randomIndex] = items[randomIndex], items[index]
	}
	return nil
}

func cryptoRandomInt(limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("random limit must be positive")
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

func buildCasualTodoSimulationText(index int, participants []devSimulationParticipant) string {
	names := make([]string, 0, len(participants))
	for _, participant := range participants {
		names = append(names, participant.Name)
	}
	assignee := names[index%len(names)]
	backup := names[(index+1)%len(names)]

	turns := []string{
		"欸明天下午三點前那個報價單誰要丟給小林啦",
		assignee + " 你幫我記一下好不好，我等下開完會會忘記",
		"可以啊但你們資料先補齊欸，少型號我沒辦法送",
		"型號在群組相簿那張，晚點七點前我傳新版給你",
		backup + " 如果我沒回你就直接打給我，拜託不要等到明天早上",
		"好啦我先把待辦記起來：明天下午三點前報價單給小林，負責人先抓 " + assignee,
		"還有會議室週五早上十點要改成大的那間，這個也順手記一下",
		"週五那個我處理，報價單不要又拖到下班才講喔",
	}
	return turns[index%len(turns)]
}
