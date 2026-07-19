package realtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"
	llminteraction "assistant-api/internal/usecase/ai/llm_interaction"
	"assistant-api/internal/usecase/ai/reranker"

	"github.com/google/uuid"
)

type stubRecentMessageStore struct {
	calls int
	items []*ent.ChannelMessage
}

func (s *stubRecentMessageStore) FindRecentMessagesBefore(ctx context.Context, message *ent.ChannelMessage, limit int) ([]*ent.ChannelMessage, error) {
	// 這個 stub 只驗證 usecase 是否真的向 repository 要近端歷史訊息；
	// message/limit 的細節已由呼叫端組裝，因此此處記錄呼叫次數即可避免測試過度耦合查詢實作。
	_ = ctx
	_ = message
	_ = limit
	s.calls++
	return s.items, nil
}

type stubContextAnalyzer struct {
	calls      int
	prompt     string
	text       string
	todoResult *llminteraction.TodoAnalysis
}

type stubImplicitReplyRanker struct {
	calls     int
	documents []string
}

func (s *stubImplicitReplyRanker) Rerank(ctx context.Context, query string, documents []string, topK int) ([]reranker.RankedDocument, error) {
	// 固定把第二筆排到第一筆前面，用來驗證 todo reminder 會尊重 reranker 回傳順序，
	// 而不是永遠使用 repository 的時間序。這能防止未來重構時不小心把精排結果丟掉。
	_ = ctx
	_ = query
	_ = topK
	s.calls++
	s.documents = append([]string(nil), documents...)
	return []reranker.RankedDocument{
		{Index: 1, Document: documents[1], Score: 0.91},
		{Index: 0, Document: documents[0], Score: 0.54},
	}, nil
}

func (s *stubContextAnalyzer) DecideFinalAction(ctx context.Context, text string, candidates []llminteraction.ActionCandidate) (*llminteraction.ActionDecision, error) {
	return nil, nil
}

func (s *stubContextAnalyzer) AnswerQuestion(ctx context.Context, text string) (*llminteraction.QuestionAnswer, error) {
	return nil, nil
}

func (s *stubContextAnalyzer) AnalyzeContext(ctx context.Context, prompt string, text string) (*llminteraction.ContextAnalysis, error) {
	return nil, nil
}

func (s *stubContextAnalyzer) AnalyzeTodo(ctx context.Context, prompt string, text string) (*llminteraction.TodoAnalysis, error) {
	// todo analyzer stub 會保存 prompt/text，讓測試可以直接檢查：
	// 1. prompt 是否包含近端候選訊息。
	// 2. text 是否仍是目前使用者輸入，而不是被替換成歷史訊息。
	_ = ctx
	s.calls++
	s.prompt = prompt
	s.text = text
	if s.todoResult != nil {
		return s.todoResult, nil
	}
	return &llminteraction.TodoAnalysis{SchemaVersion: "v1", Decision: "update_candidate", LinkedMessageID: "recent-message", Summary: "補報價單", Confidence: 0.82, Reason: "接續前文待辦"}, nil
}

func (s *stubContextAnalyzer) AnalyzeTodoDueTime(ctx context.Context, prompt string, text string) (*llminteraction.TodoDueTimeAnalysis, error) {
	_ = ctx
	_ = prompt
	_ = text
	return &llminteraction.TodoDueTimeAnalysis{SchemaVersion: "v1", Decision: "no_due_time", Precision: "unknown", Confidence: 0.5, Reason: "test stub"}, nil
}

func (s *stubContextAnalyzer) AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error) {
	return nil, nil
}

func TestTodoReminderServiceAnalyzesImplicitReplyContext(t *testing.T) {
	// 驗證核心中期流程：classifier 打出 candidate 後，todo reminder 會取最近訊息，
	// 再把目前短句與候選上下文交給 todo analyzer 做 structured decision。
	channelID := uuid.New()
	currentMessageID := uuid.New()
	recentMessageID := uuid.New()
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: recentMessageID, ChannelID: channelID, SenderName: "阿明", Content: "那報價單今天誰處理一下", CreatedAt: time.Now().Add(-time.Minute)},
	}}
	analyzer := &stubContextAnalyzer{}
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, PlatformLabel: "test", RecentLimit: 4})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", MessageType: "text", Text: "我晚點弄"},
		SavedMessage: &ent.ChannelMessage{ID: currentMessageID, ChannelID: channelID, Content: "我晚點弄", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	if repo.calls != 1 {
		t.Fatalf("expected recent message store to be called once, got %d", repo.calls)
	}
	if analyzer.calls != 1 {
		t.Fatalf("expected todo analyzer to be called once, got %d", analyzer.calls)
	}
	if analyzer.text != "我晚點弄" {
		t.Fatalf("expected analyzer text to use current message, got %q", analyzer.text)
	}
	if !strings.Contains(analyzer.prompt, recentMessageID.String()) || !strings.Contains(analyzer.prompt, "那報價單今天誰處理一下") {
		t.Fatalf("expected prompt to include recent candidate context, got %q", analyzer.prompt)
	}
	if !strings.Contains(analyzer.prompt, "todo_analysis JSON contract") {
		t.Fatalf("expected prompt to use todo analysis contract, got %q", analyzer.prompt)
	}
}

func TestBuildImplicitReplyTodoPromptRequiresArrayFields(t *testing.T) {
	// 本地小模型容易把 assignees 輸出成字串或物件；prompt 必須明確鎖住 array<string>，
	// 讓 Python validator 的 validation retry 可以修正同一份 contract，而不是放寬解析規則。
	prompt := buildImplicitReplyTodoPrompt(nil, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	if !strings.Contains(prompt, "assignees 與 missing_fields 永遠是 string array") {
		t.Fatalf("expected prompt to require string array fields, got %q", prompt)
	}
	if !strings.Contains(prompt, `"assignees":[]`) || !strings.Contains(prompt, `"missing_fields":[]`) {
		t.Fatalf("expected prompt JSON shape to show empty arrays, got %q", prompt)
	}
}

func TestBuildImplicitReplyTodoPromptTreatsReminderLanguageAsCandidate(t *testing.T) {
	// 使用者常用「提醒我」「記得」這類日常語氣建立待辦；prompt 必須明確要求 analyzer
	// 依可追蹤事項判斷，而不是因為語氣不像正式指令就回 no_action。
	prompt := buildImplicitReplyTodoPrompt(nil, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	if !strings.Contains(prompt, "日常提醒語氣") || !strings.Contains(prompt, "不可只因為語氣日常就判 no_action") {
		t.Fatalf("expected prompt to treat reminder language as todo candidate, got %q", prompt)
	}
}

func TestTodoReminderServicePersistsTodoCandidateAnalysis(t *testing.T) {
	// structured analyzer 已經完成語意判斷後，realtime service 只把固定 schema 轉成 candidate persistence input；
	// 這裡鎖住 create_candidate 會用目前訊息當 source/last message，不再停留在純 log-only。
	channelID := uuid.New()
	currentMessageID := uuid.New()
	recentMessageID := uuid.New()
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: recentMessageID, ChannelID: channelID, SenderName: "阿明", Content: "明天記得交報價單", CreatedAt: time.Now().Add(-time.Minute)},
	}}
	analyzer := &stubContextAnalyzer{todoResult: &llminteraction.TodoAnalysis{
		SchemaVersion: "v1",
		Decision:      "create_candidate",
		Summary:       "明天交報價單",
		Assignees:     []string{"我"},
		DueText:       "明天",
		Confidence:    0.91,
		Reason:        "message describes a todo",
	}}
	var persisted TodoCandidateInput
	persistCalls := 0
	service := NewTodoReminderService(TodoReminderServiceOptions{
		Repo:          repo,
		LLM:           analyzer,
		PlatformLabel: "test",
		RecentLimit:   4,
		PersistTodoCandidate: func(ctx context.Context, input TodoCandidateInput) (*ent.TodoCandidate, error) {
			_ = ctx
			persistCalls++
			persisted = input
			return &ent.TodoCandidate{ID: uuid.New()}, nil
		},
	})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", MessageType: "text", Text: "明天我來交報價單"},
		SavedMessage: &ent.ChannelMessage{ID: currentMessageID, ChannelID: channelID, Content: "明天我來交報價單", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	if persistCalls != 1 {
		t.Fatalf("expected candidate persistence to be called once, got %d", persistCalls)
	}
	if persisted.ChannelID != channelID || persisted.MessageID != currentMessageID {
		t.Fatalf("unexpected persistence message identity: %+v", persisted)
	}
	if persisted.Decision != "create_candidate" || persisted.Summary != "明天交報價單" || persisted.DueText != "明天" {
		t.Fatalf("unexpected persistence payload: %+v", persisted)
	}
	if len(persisted.Assignees) != 1 || persisted.Assignees[0] != "我" {
		t.Fatalf("unexpected assignees: %+v", persisted.Assignees)
	}
}

func TestTodoReminderServiceDoesNotPersistNoAction(t *testing.T) {
	// no_action 是 analyzer 明確判斷不要啟動 Todo Reminder；即使有 persistence function，也不能寫入候選表。
	channelID := uuid.New()
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: uuid.New(), ChannelID: channelID, SenderName: "阿明", Content: "晚上吃什麼", CreatedAt: time.Now().Add(-time.Minute)},
	}}
	analyzer := &stubContextAnalyzer{todoResult: &llminteraction.TodoAnalysis{
		SchemaVersion: "v1",
		Decision:      "no_action",
		Confidence:    0.2,
		Reason:        "chat only",
	}}
	persistCalls := 0
	service := NewTodoReminderService(TodoReminderServiceOptions{
		Repo:          repo,
		LLM:           analyzer,
		PlatformLabel: "test",
		RecentLimit:   4,
		PersistTodoCandidate: func(ctx context.Context, input TodoCandidateInput) (*ent.TodoCandidate, error) {
			_ = ctx
			_ = input
			persistCalls++
			return &ent.TodoCandidate{ID: uuid.New()}, nil
		},
	})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", MessageType: "text", Text: "哈哈好"},
		SavedMessage: &ent.ChannelMessage{ID: uuid.New(), ChannelID: channelID, Content: "哈哈好", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate, Confidence: 0.9})

	if persistCalls != 0 {
		t.Fatalf("expected no_action to skip candidate persistence, got %d calls", persistCalls)
	}
}

func TestTodoReminderServiceSkipsImplicitReplyWhenExplicitReplyExists(t *testing.T) {
	// 顯式平台 reply 已經有 reply_to_msg_id 可走既有 command/reply chain；
	// implicit linker 不應重複查最近訊息，避免同一則 reply 被兩條路徑同時判斷。
	repo := &stubRecentMessageStore{}
	analyzer := &stubContextAnalyzer{}
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, PlatformLabel: "test"})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", ReplyToMsgID: "m-1", MessageType: "text", Text: "我晚點弄"},
		SavedMessage: &ent.ChannelMessage{ID: uuid.New(), ChannelID: uuid.New(), Content: "我晚點弄", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate})

	if repo.calls != 0 {
		t.Fatalf("expected explicit reply to skip recent message query, got %d calls", repo.calls)
	}
	if analyzer.calls != 0 {
		t.Fatalf("expected explicit reply to skip todo analyzer, got %d calls", analyzer.calls)
	}
}

func TestTodoReminderServiceSkipsImplicitReplyWhenRecentLimitNotConfigured(t *testing.T) {
	// recentLimit 的預設值屬於 config 層；usecase 不應再偷偷補 8。
	// 若測試或啟動流程沒有注入有效值，就直接略過 implicit linker，讓設定錯誤能在 log 中被看見。
	repo := &stubRecentMessageStore{}
	analyzer := &stubContextAnalyzer{}
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, PlatformLabel: "test"})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-2", MessageType: "text", Text: "我晚點弄"},
		SavedMessage: &ent.ChannelMessage{ID: uuid.New(), ChannelID: uuid.New(), Content: "我晚點弄", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate})

	if repo.calls != 0 {
		t.Fatalf("expected missing recent limit to skip recent message query, got %d calls", repo.calls)
	}
	if analyzer.calls != 0 {
		t.Fatalf("expected missing recent limit to skip todo analyzer, got %d calls", analyzer.calls)
	}
}

func TestTodoReminderServiceUsesRerankedImplicitReplyCandidates(t *testing.T) {
	// 驗證「召回窗口」和「語意排序」是兩階段：repository 先回時間序候選，
	// reranker 再依目前訊息把更相關的候選排前面，最後 prompt 必須使用精排後順序。
	channelID := uuid.New()
	olderMessageID := uuid.New()
	newerMessageID := uuid.New()
	repo := &stubRecentMessageStore{items: []*ent.ChannelMessage{
		{ID: olderMessageID, ChannelID: channelID, SenderName: "阿明", Content: "等等有人幫我買咖啡嗎", CreatedAt: time.Now().Add(-2 * time.Minute)},
		{ID: newerMessageID, ChannelID: channelID, SenderName: "小美", Content: "明天早上記得交報價單", CreatedAt: time.Now().Add(-time.Minute)},
	}}
	analyzer := &stubContextAnalyzer{}
	ranker := &stubImplicitReplyRanker{}
	service := NewTodoReminderService(TodoReminderServiceOptions{Repo: repo, LLM: analyzer, Ranker: ranker, PlatformLabel: "test", RecentLimit: 4})

	service.HandleClassification(context.Background(), MessageContext{
		Message:      &unifiedmessage.Message{ChannelID: "channel-1", PlatformMessageID: "m-3", MessageType: "text", Text: "我晚點補"},
		SavedMessage: &ent.ChannelMessage{ID: uuid.New(), ChannelID: channelID, Content: "我晚點補", CreatedAt: time.Now()},
	}, ClassificationResult{Tag: "todo", Signal: ClassificationSignalCandidate})

	if ranker.calls != 1 {
		t.Fatalf("expected reranker to be called once, got %d", ranker.calls)
	}
	firstIndex := strings.Index(analyzer.prompt, newerMessageID.String())
	secondIndex := strings.Index(analyzer.prompt, olderMessageID.String())
	if firstIndex < 0 || secondIndex < 0 || firstIndex > secondIndex {
		t.Fatalf("expected prompt to use reranked order, got %q", analyzer.prompt)
	}
}
