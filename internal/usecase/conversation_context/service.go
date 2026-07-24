package conversationcontext

import (
	"context"
	"fmt"
	"strings"
	"time"

	"assistant-api/internal/ent"
	"assistant-api/internal/repository"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"

	"github.com/google/uuid"
)

// Operation 是 context-aware 指令在 action decision 層使用的 api_operation。
const Operation = "channel_context_query"

// Config 控制 context retrieval 與 prompt 大小。
type Config struct {
	RecentMessageLimit int
	MaxContextMessages int
	MaxContextChars    int
	ExcludedSenderIDs  []string
}

// LLM 定義本功能需要的最小 AI 能力。
// MVP 直接使用既有問答模型；系統把「任務 + 對話上下文」組成單一輸入文字，避免新增 transport contract。
type LLM interface {
	AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error)
}

// Service 執行 channel conversation context query。
type Service struct {
	repo   *repository.ChannelMessageRepo
	llm    LLM
	config Config
}

// Message 是 prompt 內部使用的對話訊息 snapshot。
type Message struct {
	Index             int
	ID                uuid.UUID
	SenderName        string
	SenderID          string
	Text              string
	PlatformMessageID string
	PlatformTimestamp int64
	CreatedAt         time.Time
}

// PreviewResult 描述同一套 retrieval/prompt builder 會餵給 AI 的內容。
type PreviewResult struct {
	Task            string
	Messages        []Message
	PromptText      string
	RecentLimit     int
	SelectedLimit   int
	MaxContextChars int
}

// ExecuteResult 是正式 action 執行後的結果。
type ExecuteResult struct {
	Preview    PreviewResult
	Answer     string
	Confidence float64
}

// New 建立 conversation context service。
func New(repo *repository.ChannelMessageRepo, llm LLM, config Config) *Service {
	config = normalizeConfig(config)
	return &Service{repo: repo, llm: llm, config: config}
}

// Preview 只做 retrieval 與 prompt 組裝，不呼叫模型；供 dev helper 與測試快速檢查資料來源。
func (s *Service) Preview(ctx context.Context, sourceMessage *ent.ChannelMessage, task string) (*PreviewResult, error) {
	return s.preview(ctx, sourceMessage, task, s.config.ExcludedSenderIDs)
}

func (s *Service) preview(ctx context.Context, sourceMessage *ent.ChannelMessage, task string, excludedSenderIDs []string) (*PreviewResult, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("conversation context service is not initialized")
	}
	task = strings.TrimSpace(task)
	if task == "" {
		return nil, fmt.Errorf("context task is required")
	}
	if sourceMessage == nil || sourceMessage.ID == uuid.Nil {
		return nil, fmt.Errorf("source message is required")
	}

	items, err := s.repo.FindRecentMessagesBefore(ctx, sourceMessage, s.config.RecentMessageLimit)
	if err != nil {
		return nil, err
	}
	messages := selectPromptMessages(items, s.config.MaxContextMessages, s.config.MaxContextChars, excludedSenderIDs, []string{sourceMessage.PlatformMessageID})
	preview := &PreviewResult{
		Task:            task,
		Messages:        messages,
		PromptText:      BuildPromptText(task, messages),
		RecentLimit:     s.config.RecentMessageLimit,
		SelectedLimit:   s.config.MaxContextMessages,
		MaxContextChars: s.config.MaxContextChars,
	}
	return preview, nil
}

// Execute 以目前 channel 近端對話作為資料來源，要求 AI 回答使用者任務。
func (s *Service) Execute(ctx context.Context, sourceMessage *ent.ChannelMessage, task string) (*ExecuteResult, error) {
	return s.ExecuteWithExcludedSenderIDs(ctx, sourceMessage, task, nil)
}

// ExecuteWithExcludedSenderIDs 執行正式查詢，並可額外排除指定 sender id。
func (s *Service) ExecuteWithExcludedSenderIDs(ctx context.Context, sourceMessage *ent.ChannelMessage, task string, excludedSenderIDs []string) (*ExecuteResult, error) {
	if s == nil || s.llm == nil {
		return nil, fmt.Errorf("conversation context llm is not initialized")
	}
	preview, err := s.preview(ctx, sourceMessage, task, mergeSenderIDs(s.config.ExcludedSenderIDs, excludedSenderIDs))
	if err != nil {
		return nil, err
	}
	if len(preview.Messages) == 0 {
		return nil, fmt.Errorf("no previous channel messages are available for context")
	}
	answer, err := s.llm.AnswerQuestion(ctx, preview.PromptText)
	if err != nil {
		return nil, err
	}
	if answer == nil || strings.TrimSpace(answer.Answer) == "" {
		return nil, fmt.Errorf("conversation context answer is empty")
	}
	return &ExecuteResult{Preview: *preview, Answer: strings.TrimSpace(answer.Answer), Confidence: answer.Confidence}, nil
}

// BuildPromptText 將任務與 selected messages 組成可直接送問答模型的輸入。
func BuildPromptText(task string, messages []Message) string {
	var builder strings.Builder
	builder.WriteString("你要根據下方 conversation_context 回答使用者任務。\n")
	builder.WriteString("規則：\n")
	builder.WriteString("- 只能使用 conversation_context 中提供的訊息作為資料來源。\n")
	builder.WriteString("- conversation_context 可能包含使用者指令與助理回覆；請依任務判斷哪些訊息相關，不要因訊息來自助理或包含 mention 就自動忽略。\n")
	builder.WriteString("- 回答大家、群組、頻道成員、這裡的人等群體討論問題時，必須區分使用者訊息與助理回覆；助理提出的建議、追問或先前回答，不等於群組成員已討論、同意或決定。\n")
	builder.WriteString("- 若只有助理回覆提到某個選項或方向，而使用者訊息沒有支持，請說明那是助理回覆中的建議，不可把它當成大家的共識或討論結果。\n")
	builder.WriteString("- 若有重複或無關訊息，請自行忽略並聚焦回答任務。\n")
	builder.WriteString("- 如果資料不足，請明確說明缺少哪些資訊，不要猜測。\n")
	builder.WriteString("- 若需要計算金額、數量或分攤，請列出計算依據。\n")
	builder.WriteString("- 使用繁體中文回答。\n\n")
	builder.WriteString("使用者任務：\n")
	builder.WriteString(strings.TrimSpace(task))
	builder.WriteString("\n\nconversation_context:\n")
	for _, message := range messages {
		builder.WriteString(fmt.Sprintf("[%d] %s: %s\n", message.Index, displayName(message), strings.TrimSpace(message.Text)))
	}
	return strings.TrimSpace(builder.String())
}

func normalizeConfig(config Config) Config {
	if config.RecentMessageLimit <= 0 {
		config.RecentMessageLimit = 40
	}
	if config.MaxContextMessages <= 0 {
		config.MaxContextMessages = 30
	}
	if config.MaxContextMessages > config.RecentMessageLimit {
		config.MaxContextMessages = config.RecentMessageLimit
	}
	if config.MaxContextChars <= 0 {
		config.MaxContextChars = 12000
	}
	config.ExcludedSenderIDs = normalizeSenderIDs(config.ExcludedSenderIDs)
	return config
}

func selectPromptMessages(items []*ent.ChannelMessage, maxMessages int, maxChars int, excludedSenderIDs []string, excludedPlatformMessageIDs []string) []Message {
	if len(items) == 0 || maxMessages <= 0 || maxChars <= 0 {
		return nil
	}
	// 選取策略只做資料品質過濾，不做語意剪枝：
	// - bot/mention/command-like 訊息仍可能是使用者查詢「上面對話」時的重要脈絡。
	// - 真正哪些內容相關，交給 prompt 裡的規則與模型判斷。
	// - 這裡只排除空白、非文字、指定 sender、來源訊息本身與 Slack retry 造成的重複 platform_message_id。
	excluded := senderIDSet(excludedSenderIDs)
	excludedPlatformMessages := stringSet(excludedPlatformMessageIDs)
	seenPlatformMessages := map[string]struct{}{}
	eligible := make([]*ent.ChannelMessage, 0, len(items))
	for _, item := range items {
		if item == nil || !strings.EqualFold(strings.TrimSpace(item.MessageType), "text") {
			continue
		}
		platformMessageID := strings.TrimSpace(item.PlatformMessageID)
		if platformMessageID != "" {
			if _, skip := excludedPlatformMessages[platformMessageID]; skip {
				continue
			}
			if _, duplicate := seenPlatformMessages[platformMessageID]; duplicate {
				continue
			}
			seenPlatformMessages[platformMessageID] = struct{}{}
		}
		if _, skip := excluded[strings.TrimSpace(item.SenderID)]; skip {
			continue
		}
		if strings.TrimSpace(item.Content) == "" {
			continue
		}
		eligible = append(eligible, item)
	}
	start := 0
	if len(eligible) > maxMessages {
		start = len(eligible) - maxMessages
	}
	// Repository 回傳的是 source message 之前的時間序列；截取尾端代表保留最靠近指令的上下文。
	// 字元限制採「停止加入後續訊息」而不是截斷單則訊息，避免把半句話送進模型導致語意失真。
	selected := make([]Message, 0, len(eligible)-start)
	usedChars := 0
	for _, item := range eligible[start:] {
		text := strings.TrimSpace(item.Content)
		if usedChars+len([]rune(text)) > maxChars {
			break
		}
		usedChars += len([]rune(text))
		selected = append(selected, Message{
			Index:             len(selected) + 1,
			ID:                item.ID,
			SenderName:        strings.TrimSpace(item.SenderName),
			SenderID:          strings.TrimSpace(item.SenderID),
			Text:              text,
			PlatformMessageID: strings.TrimSpace(item.PlatformMessageID),
			PlatformTimestamp: item.PlatformTimestamp,
			CreatedAt:         item.CreatedAt,
		})
	}
	return selected
}

func mergeSenderIDs(primary []string, secondary []string) []string {
	merged := make([]string, 0, len(primary)+len(secondary))
	merged = append(merged, primary...)
	merged = append(merged, secondary...)
	return normalizeSenderIDs(merged)
}

func normalizeSenderIDs(senderIDs []string) []string {
	if len(senderIDs) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(senderIDs))
	for _, senderID := range senderIDs {
		trimmed := strings.TrimSpace(senderID)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		normalized = append(normalized, trimmed)
	}
	return normalized
}

func senderIDSet(senderIDs []string) map[string]struct{} {
	return stringSet(normalizeSenderIDs(senderIDs))
}

func stringSet(values []string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		set[trimmed] = struct{}{}
	}
	return set
}

func displayName(message Message) string {
	if strings.TrimSpace(message.SenderName) != "" {
		return strings.TrimSpace(message.SenderName)
	}
	if strings.TrimSpace(message.SenderID) != "" {
		return strings.TrimSpace(message.SenderID)
	}
	return "unknown"
}
