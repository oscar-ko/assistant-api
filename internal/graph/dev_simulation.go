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

	"assistant-api/internal/config"
	"assistant-api/internal/ent/slack"
	"assistant-api/internal/ent/slackworkspace"
	"assistant-api/internal/graph/model"
	"assistant-api/internal/integration/runtimecontext"

	"github.com/google/uuid"
)

type devSimulationParticipant struct {
	UserID         uuid.UUID
	PlatformUserID string
	Name           string
}

type devSimulationTurnPlanInput struct {
	Index                     int
	Platform                  string
	DeliveryMode              string
	DefaultPlatformAppID      string
	PlatformTenantID          string
	Script                    []*model.SimulateTodoConversationMessageInput
	Participants              []devSimulationParticipant
	SenderParticipant         *devSimulationParticipant
	PlatformMessageIDs        []string
	VisiblePlatformMessageIDs []string
}

type devSimulationTurnPlan struct {
	Participant                     devSimulationParticipant
	Text                            string
	VisiblePlatformAppID            string
	ReplyToPlatformMessageID        string
	ReplyToVisiblePlatformMessageID string
}

func normalizeSimulationBaseMessageIDs(values []string) []string {
	items := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		items = append(items, trimmed)
	}
	return items
}

// buildSimulatedTodoTurnPlan 將「這一輪訊息要怎麼送」集中成單一決策點。
// 這裡同時處理 scripted participant、senderUserID override、visible Slack app 選擇、
// 以及 replyToMessageIndex 對應出的 internal/visible 兩種 message id。
// resolver loop 只負責執行 plan，避免未來新增 scenario 時把 sender/reply/visible 規則散落各處。
func (r *Resolver) buildSimulatedTodoTurnPlan(ctx context.Context, input devSimulationTurnPlanInput) (devSimulationTurnPlan, error) {
	participantIndex, text, replyToMessageIndex, err := resolveSimulatedTodoScriptTurn(input.Index, input.Script, input.Participants)
	if err != nil {
		return devSimulationTurnPlan{}, err
	}
	participant := input.Participants[participantIndex]
	if input.SenderParticipant != nil {
		participant = *input.SenderParticipant
	}

	visiblePlatformAppID := ""
	if strings.EqualFold(strings.TrimSpace(input.Platform), "slack") {
		visiblePlatformAppID = resolveSlackScriptPlatformAppID(input.DefaultPlatformAppID, input.Script, input.Index)
		if input.SenderParticipant == nil {
			// 明確指定 Slack app 的 script 代表「外部未註冊長官/同事 bot」在發話；
			// 因此沒有 senderUserID override 時，內部 sender 要用該 Slack bot user id，
			// 讓 ChannelMessage.sender_user_id 保持空值，避免誤認成已註冊使用者本人。
			if input.DeliveryMode == "visible" || hasSlackScriptPlatformAppID(input.Script, input.Index) {
				botParticipant, err := r.resolveSlackBotSimulationParticipant(ctx, input.PlatformTenantID, visiblePlatformAppID)
				if err != nil {
					return devSimulationTurnPlan{}, err
				}
				participant = botParticipant
			}
		}
	}

	replyToPlatformMessageID := ""
	replyToVisiblePlatformMessageID := ""
	if replyToMessageIndex != nil {
		if *replyToMessageIndex < 0 || *replyToMessageIndex >= len(input.PlatformMessageIDs) {
			return devSimulationTurnPlan{}, fmt.Errorf("messages[%d].replyToMessageIndex must point to an earlier message", input.Index)
		}
		replyToPlatformMessageID = input.PlatformMessageIDs[*replyToMessageIndex]
		if *replyToMessageIndex < len(input.VisiblePlatformMessageIDs) {
			replyToVisiblePlatformMessageID = input.VisiblePlatformMessageIDs[*replyToMessageIndex]
		}
	}

	return devSimulationTurnPlan{
		Participant:                     participant,
		Text:                            text,
		VisiblePlatformAppID:            visiblePlatformAppID,
		ReplyToPlatformMessageID:        replyToPlatformMessageID,
		ReplyToVisiblePlatformMessageID: replyToVisiblePlatformMessageID,
	}, nil
}

func (r *Resolver) resolveSlackBotSimulationParticipant(ctx context.Context, platformTenantID string, platformAppID string) (devSimulationParticipant, error) {
	bot, err := config.Slack.BotByAppID(strings.TrimSpace(platformAppID))
	if err != nil {
		return devSimulationParticipant{}, err
	}
	botUserID := ""
	if r != nil && r.Client != nil {
		// Slack bot user id 是 workspace install 的結果，同一個 app 在不同 workspace 可能不同；
		// 不從 app.yml 讀 fallback，避免靜態設定與 OAuth install 狀態不一致。
		workspace, err := r.Client.SlackWorkspace.Query().Where(slackworkspace.AppIDEQ(strings.TrimSpace(platformAppID)), slackworkspace.PlatformTeamIDEQ(strings.TrimSpace(platformTenantID))).Only(ctx)
		if err != nil {
			return devSimulationParticipant{}, fmt.Errorf("query slack workspace bot user id failed: %w", err)
		}
		if workspace.BotUserID != nil {
			botUserID = strings.TrimSpace(*workspace.BotUserID)
		}
	}
	if botUserID == "" {
		return devSimulationParticipant{}, fmt.Errorf("slack workspace bot_user_id is empty for app %s team %s", strings.TrimSpace(platformAppID), strings.TrimSpace(platformTenantID))
	}
	name := strings.TrimSpace(bot.Name)
	if name == "" {
		name = strings.TrimSpace(bot.AppID)
	}
	return devSimulationParticipant{PlatformUserID: botUserID, Name: name}, nil
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
	ThreadTS    string `json:"thread_ts,omitempty"`
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

func (r *Resolver) pickSlackBotScriptParticipants(ctx context.Context, platformTenantID string, defaultPlatformAppID string, script []*model.SimulateTodoConversationMessageInput, count int) ([]devSimulationParticipant, error) {
	if count < 1 {
		return nil, fmt.Errorf("participantCount must be at least 1")
	}
	appIDs := make([]string, 0, count)
	seen := map[string]struct{}{}
	addApp := func(appID string) {
		appID = strings.TrimSpace(appID)
		if appID == "" {
			return
		}
		key := strings.ToLower(appID)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		appIDs = append(appIDs, appID)
	}
	for _, message := range script {
		if message == nil {
			continue
		}
		addApp(trimOptionalString(message.VisiblePlatformAppID))
		if len(appIDs) >= count {
			break
		}
	}
	addApp(defaultPlatformAppID)
	if len(appIDs) < count {
		return nil, fmt.Errorf("not enough slack bot apps in script: need %d, got %d", count, len(appIDs))
	}
	participants := make([]devSimulationParticipant, 0, count)
	for _, appID := range appIDs[:count] {
		participant, err := r.resolveSlackBotSimulationParticipant(ctx, platformTenantID, appID)
		if err != nil {
			return nil, err
		}
		participants = append(participants, participant)
	}
	return participants, nil
}

func hasSlackScriptPlatformAppIDs(script []*model.SimulateTodoConversationMessageInput) bool {
	for _, message := range script {
		if message != nil && trimOptionalString(message.VisiblePlatformAppID) != "" {
			return true
		}
	}
	return false
}

func (r *Resolver) resolveSimulationSenderParticipant(ctx context.Context, platform string, platformTenantID string, senderUserID *uuid.UUID) (*devSimulationParticipant, error) {
	if senderUserID == nil {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "slack":
		// senderUserID 是「刻意要用已註冊使用者身份」的 override；
		// boss-bot / 未註冊外部角色情境不應帶這個欄位，會改由 visible bot identity 決定 sender。
		teamID := strings.TrimSpace(platformTenantID)
		if teamID == "" {
			return nil, fmt.Errorf("slack platformTenantID is required")
		}
		item, err := r.Client.Slack.Query().Where(slack.PlatformTeamIDEQ(teamID), slack.UserIDEQ(*senderUserID)).WithUser().Only(ctx)
		if err != nil {
			return nil, fmt.Errorf("query slack sender user failed: %w", err)
		}
		user, err := item.Edges.UserOrErr()
		if err != nil {
			return nil, fmt.Errorf("slack sender user edge is not loaded: %w", err)
		}
		name := strings.TrimSpace(user.Name)
		if item.DisplayName != nil && strings.TrimSpace(*item.DisplayName) != "" {
			name = strings.TrimSpace(*item.DisplayName)
		}
		if name == "" {
			return nil, fmt.Errorf("slack sender user display name is empty: %s", user.ID.String())
		}
		return &devSimulationParticipant{UserID: user.ID, PlatformUserID: strings.TrimSpace(item.PlatformUserID), Name: name}, nil
	case "line":
		return nil, fmt.Errorf("senderUserID override currently supports slack simulation only")
	default:
		return nil, fmt.Errorf("unsupported simulation platform: %s", platform)
	}
}

func (r *Resolver) buildSimulatedWebhookBody(platform string, platformTenantID string, platformChannelID string, participant devSimulationParticipant, text string, replyToPlatformMessageID string, timestamp int64) (string, []byte, error) {
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
		body, err := marshalSimulatedSlackWebhook(platformTenantID, participant, platformChannelID, "channel", platformMessageID, text, replyToPlatformMessageID)
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

func marshalSimulatedSlackWebhook(teamID string, participant devSimulationParticipant, platformChannelID string, channelType string, platformMessageID string, text string, replyToPlatformMessageID string) ([]byte, error) {
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
			ThreadTS:    strings.TrimSpace(replyToPlatformMessageID),
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

func normalizeTodoSimulationDeliveryMode(value string) (string, error) {
	// deliveryMode 目前只接受兩種明確模式：
	// internal 用於 AI/自動化快速回歸；visible 用於把同一份劇本同步貼到真實 Slack channel 供人工觀察。
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "internal":
		return "internal", nil
	case "visible":
		return "visible", nil
	default:
		return "", fmt.Errorf("unsupported simulation deliveryMode: %s", value)
	}
}

func (r *Resolver) sendVisibleSlackSimulationMessage(ctx context.Context, platformTenantID string, platformAppID string, platformChannelID string, participant devSimulationParticipant, text string, replyToVisiblePlatformMessageID string) (string, error) {
	if r == nil || r.SlackPushService == nil {
		return "", fmt.Errorf("slack push service is not initialized")
	}
	visibleText := strings.TrimSpace(text)
	// runtime context 指定 workspace team/app，讓 Slack push service 用正確 install token 發訊息；
	// replyToVisiblePlatformMessageID 會成為 Slack thread_ts，用來測試人工可見的 thread/reply 形狀。
	messageCtx := runtimecontext.WithWorkspaceAppID(runtimecontext.WithWorkspaceTeamID(ctx, platformTenantID), platformAppID)
	return r.SlackPushService.SendTextToChat(messageCtx, platformChannelID, "", visibleText, replyToVisiblePlatformMessageID, "")
}

func resolveSlackScriptPlatformAppID(defaultPlatformAppID string, script []*model.SimulateTodoConversationMessageInput, index int) string {
	if index >= 0 && index < len(script) && script[index] != nil {
		if value := trimOptionalString(script[index].VisiblePlatformAppID); value != "" {
			return value
		}
	}
	return strings.TrimSpace(defaultPlatformAppID)
}

func hasSlackScriptPlatformAppID(script []*model.SimulateTodoConversationMessageInput, index int) bool {
	return index >= 0 && index < len(script) && script[index] != nil && trimOptionalString(script[index].VisiblePlatformAppID) != ""
}

func resolveSimulatedTodoScriptTurn(index int, script []*model.SimulateTodoConversationMessageInput, participants []devSimulationParticipant) (int, string, *int, error) {
	participantCount := len(participants)
	if participantCount <= 0 {
		return 0, "", nil, fmt.Errorf("participant count must be positive")
	}
	if len(script) == 0 {
		return index % participantCount, buildCasualTodoSimulationText(index, participants), nil, nil
	}
	if index < 0 || index >= len(script) || script[index] == nil {
		return 0, "", nil, fmt.Errorf("messages[%d] is required", index)
	}
	turn := script[index]
	participantIndex := index % participantCount
	if turn.ParticipantIndex != nil {
		participantIndex = *turn.ParticipantIndex
	}
	if participantIndex < 0 || participantIndex >= participantCount {
		return 0, "", nil, fmt.Errorf("messages[%d].participantIndex is out of range", index)
	}
	text := strings.TrimSpace(turn.Text)
	if text == "" {
		return 0, "", nil, fmt.Errorf("messages[%d].text is required", index)
	}
	return participantIndex, text, turn.ReplyToMessageIndex, nil
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
