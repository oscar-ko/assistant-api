package topkfilter

import (
	"context"
	"encoding/json"
	"testing"

	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"

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

func (s *stubSearcher) SearchTopByVectorAndLocale(ctx context.Context, locale string, queryVector string, topK int, skillCodes []string) ([]repository.ActionRouteVectorCandidate, error) {
	_ = ctx
	_ = skillCodes
	s.called = true
	s.locale = locale
	s.queryVector = queryVector
	s.topK = topK
	return []repository.ActionRouteVectorCandidate{{
		APIOperation: "start_translation_locale",
		SkillCode:    "channel.translation",
		RouteText:    "開啟翻譯",
		Distance:     0.123456,
	}}, nil
}

type stubEmbedder struct{}

func (stubEmbedder) GetEmbedding(ctx context.Context, text string) ([]float64, error) {
	_ = ctx
	_ = text
	return []float64{0.1, 0.2, 0.3}, nil
}

func TestFilterMessageLogsTopKCandidates(t *testing.T) {
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

	if !searcher.called {
		t.Fatalf("expected vector search to be called")
	}
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

	entries := observed.FilterMessage("line message top-k filtered candidates").All()
	if len(entries) != 1 {
		t.Fatalf("expected one top-k filter log, got %d", len(entries))
	}
	if got := entries[0].ContextMap()["top_k"]; got != int64(3) {
		t.Fatalf("logged top_k = %v, want 3", got)
	}
}
