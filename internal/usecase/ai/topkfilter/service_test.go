package topkfilter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/ai/reranker"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type stubSearcher struct {
	locale      string
	queryVector string
	topK        int
	called      bool
}

// stubSearcher 固定回傳兩筆候選，模擬 retrieval stage 的查詢結果。
func (s *stubSearcher) SearchTopByVectorAndLocale(ctx context.Context, locale string, queryVector string, topK int, skillCodes []string) ([]repository.ActionRouteVectorCandidate, error) {
	_ = ctx
	_ = skillCodes
	s.called = true
	s.locale = locale
	s.queryVector = queryVector
	s.topK = topK
	return []repository.ActionRouteVectorCandidate{
		{
			APIOperation: "start_translation_locale",
			SkillCode:    "channel.translation",
			RouteText:    "開啟翻譯",
			Distance:     0.123456,
		},
		{
			APIOperation: "create_todo_task",
			SkillCode:    "todo.reminder",
			RouteText:    "新增待辦",
			Distance:     0.223456,
		},
	}, nil
}

type stubEmbedder struct{}

// stubEmbedder 提供穩定向量，避免測試受外部模型影響。
func (stubEmbedder) GetEmbedding(ctx context.Context, text string) ([]float64, error) {
	_ = ctx
	_ = text
	return []float64{0.1, 0.2, 0.3}, nil
}

type stubReranker struct {
	called bool
}

// stubReranker 固定把第二名候選排到第一名，驗證重排是否生效。
func (s *stubReranker) Rerank(ctx context.Context, query string, documents []string, topK int) ([]reranker.RankedDocument, error) {
	_ = ctx
	_ = query
	_ = documents
	_ = topK
	s.called = true
	return []reranker.RankedDocument{
		{Index: 1, Document: "新增待辦", Score: 0.91},
		{Index: 0, Document: "開啟翻譯", Score: 0.77},
	}, nil
}

// TestFilterMessageLogsTopKCandidates 驗證 retrieval 後仍會輸出 top-k 結果 log。
func TestFilterMessageLogsTopKCandidates(t *testing.T) {
	// 以 observer logger 攔截結構化日誌，驗證 top-k 流程是否有輸出預期觀測資料。
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)

	searcher := &stubSearcher{}
	svc := NewService(searcher, stubEmbedder{}, "zh-TW", 3)
	svc.FilterMessage(context.Background(), &unifiedmessage.Message{
		ChannelID:         "C123",
		PlatformMessageID: "M123",
		MessageType:       "text",
		Text:              "幫我開啟翻譯",
	})

	// 確認 retrieval stage 的 repository 查詢已被觸發。
	if !searcher.called {
		t.Fatalf("expected vector search to be called")
	}
	// 確認 locale/topK 參數有正確傳遞到查詢層。
	if searcher.locale != "zh-TW" {
		t.Fatalf("locale = %q, want zh-TW", searcher.locale)
	}
	if searcher.topK != 3 {
		t.Fatalf("topK = %d, want 3", searcher.topK)
	}

	var decoded []float64
	if err := json.Unmarshal([]byte(searcher.queryVector), &decoded); err != nil {
		t.Fatalf("query vector should be valid json: %v", err)
	}

	// 驗證 retrieval 與 reranked 兩段日誌都存在，方便比較 rerank 前後差異。
	entries := observed.FilterMessage("inbound message top-k reranked candidates").All()
	if len(entries) != 1 {
		t.Fatalf("expected one top-k reranked log, got %d", len(entries))
	}
	retrieved := observed.FilterMessage("inbound message top-k retrieved candidates").All()
	if len(retrieved) != 1 {
		t.Fatalf("expected one top-k retrieved log, got %d", len(retrieved))
	}
	if got := entries[0].ContextMap()["top_k"]; got != int64(3) {
		t.Fatalf("logged top_k = %v, want 3", got)
	}
}

// TestFilterMessageUsesRerankerWhenAvailable 驗證有注入 reranker 時會套用重排。
func TestFilterMessageUsesRerankerWhenAvailable(t *testing.T) {
	// 同樣用 observer logger 驗證 rerank 開啟時的輸出內容。
	core, observed := observer.New(zapcore.DebugLevel)
	oldLogger := zap.L()
	zap.ReplaceGlobals(zap.New(core))
	defer zap.ReplaceGlobals(oldLogger)

	searcher := &stubSearcher{}
	rerankerStub := &stubReranker{}
	svc := NewServiceWithReranker(searcher, stubEmbedder{}, rerankerStub, "zh-TW", 2)
	svc.FilterMessage(context.Background(), &unifiedmessage.Message{
		ChannelID:         "C123",
		PlatformMessageID: "M456",
		MessageType:       "text",
		Text:              "幫我建立待辦",
	})

	// 確認第二階段 reranker 確實被呼叫。
	if !rerankerStub.called {
		t.Fatalf("expected reranker to be called")
	}

	entries := observed.FilterMessage("inbound message top-k reranked candidates").All()
	if len(entries) != 1 {
		t.Fatalf("expected one top-k reranked log, got %d", len(entries))
	}
	retrieved := observed.FilterMessage("inbound message top-k retrieved candidates").All()
	if len(retrieved) != 1 {
		t.Fatalf("expected one top-k retrieved log, got %d", len(retrieved))
	}
	candidatesValue, ok := entries[0].ContextMap()["candidates"]
	if !ok {
		t.Fatalf("expected candidates field in log")
	}
	candidatesText := strings.TrimSpace(fmt.Sprint(candidatesValue))
	if candidatesText == "" {
		t.Fatalf("expected candidates to be non-empty")
	}
	if !containsAll(candidatesText, []string{"operation=create_todo_task", "rerank_score="}) {
		t.Fatalf("expected reranked todo route in candidates, got %q", candidatesText)
	}
	comparisonValue, ok := entries[0].ContextMap()["rank_comparison"]
	if !ok {
		t.Fatalf("expected rank_comparison field in log")
	}
	comparisonText := strings.TrimSpace(fmt.Sprint(comparisonValue))
	if !containsAll(comparisonText, []string{"op=create_todo_task", "2->1 (+1)"}) {
		t.Fatalf("expected rank comparison to show rerank movement, got %q", comparisonText)
	}
}

func containsAll(text string, parts []string) bool {
	for _, part := range parts {
		if part == "" {
			continue
		}
		if !strings.Contains(text, part) {
			return false
		}
	}
	return true
}
