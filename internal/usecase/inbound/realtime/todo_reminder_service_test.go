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
	calls  int
	prompt string
	text   string
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
	// context analyzer stub 會保存 prompt/text，讓測試可以直接檢查：
	// 1. prompt 是否包含近端候選訊息。
	// 2. text 是否仍是目前使用者輸入，而不是被替換成歷史訊息。
	_ = ctx
	s.calls++
	s.prompt = prompt
	s.text = text
	return &llminteraction.ContextAnalysis{SchemaVersion: "v1", Decision: "relevant", TargetService: "todo_reminder", Confidence: 0.82, Reason: "接續前文待辦"}, nil
}

func (s *stubContextAnalyzer) AskClarifyingQuestion(ctx context.Context, text string, reason string) (*llminteraction.QuestionAnswer, error) {
	return nil, nil
}

func TestTodoReminderServiceAnalyzesImplicitReplyContext(t *testing.T) {
	// 驗證核心中期流程：classifier 打出 candidate 後，todo reminder 會取最近訊息，
	// 再把目前短句與候選上下文交給 context analyzer 做 structured decision。
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
		t.Fatalf("expected context analyzer to be called once, got %d", analyzer.calls)
	}
	if analyzer.text != "我晚點弄" {
		t.Fatalf("expected analyzer text to use current message, got %q", analyzer.text)
	}
	if !strings.Contains(analyzer.prompt, recentMessageID.String()) || !strings.Contains(analyzer.prompt, "那報價單今天誰處理一下") {
		t.Fatalf("expected prompt to include recent candidate context, got %q", analyzer.prompt)
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
		t.Fatalf("expected explicit reply to skip context analyzer, got %d calls", analyzer.calls)
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
