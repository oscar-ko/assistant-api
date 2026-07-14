package ranking

import (
	"context"
	"testing"

	"assistant-api/internal/repository"
)

type stubRetriever struct {
	candidates []repository.ActionRouteVectorCandidate
}

func (s stubRetriever) Retrieve(ctx context.Context, query string) ([]repository.ActionRouteVectorCandidate, error) {
	// 測試用 retriever：每次回傳副本，避免後續 stage 直接改到原切片。
	_ = ctx
	_ = query
	out := make([]repository.ActionRouteVectorCandidate, 0, len(s.candidates))
	out = append(out, s.candidates...)
	return out, nil
}

type keepFirstStage struct{}

func (keepFirstStage) Apply(ctx context.Context, query string, candidates []repository.ActionRouteVectorCandidate) ([]ScoredCandidate, bool, error) {
	// 第一個 stage 只保留首筆，模擬「硬裁切候選」行為。
	_ = ctx
	_ = query
	if len(candidates) == 0 {
		return buildScoredCandidates(candidates, nil), false, nil
	}
	return buildScoredCandidates(candidates[:1], nil), true, nil
}

type scoreStage struct{}

func (scoreStage) Apply(ctx context.Context, query string, candidates []repository.ActionRouteVectorCandidate) ([]ScoredCandidate, bool, error) {
	// 第二個 stage 不改順序，只補分數，驗證 stage 可串接並覆蓋 score 輸出。
	_ = ctx
	_ = query
	if len(candidates) == 0 {
		return buildScoredCandidates(candidates, nil), false, nil
	}
	return buildScoredCandidates(candidates, []float64{0.99}), true, nil
}

func TestCandidatePipelineWithStagesRunsInOrder(t *testing.T) {
	// 這個測試驗證兩件事：
	// 1) RetrieveCandidates 由 retriever 原樣回傳。
	// 2) RerankCandidates 會按 stage 順序執行並反映最終結果。
	retriever := stubRetriever{candidates: []repository.ActionRouteVectorCandidate{{APIOperation: "first"}, {APIOperation: "second"}}}
	pipeline := NewCandidatePipelineWithStages(retriever, keepFirstStage{}, scoreStage{})
	if pipeline == nil {
		t.Fatalf("expected pipeline to be created")
	}

	retrieved, err := pipeline.RetrieveCandidates(context.Background(), "hello")
	if err != nil {
		t.Fatalf("retrieve failed: %v", err)
	}
	if len(retrieved) != 2 {
		t.Fatalf("retrieved len = %d, want 2", len(retrieved))
	}

	reordered, applied, err := pipeline.RerankCandidates(context.Background(), "hello", retrieved)
	if err != nil {
		t.Fatalf("rerank failed: %v", err)
	}
	if !applied {
		t.Fatalf("expected stages to be applied")
	}
	if len(reordered) != 1 {
		t.Fatalf("reordered len = %d, want 1", len(reordered))
	}
	if reordered[0].Candidate.APIOperation != "first" {
		t.Fatalf("unexpected operation = %q", reordered[0].Candidate.APIOperation)
	}
	if reordered[0].Score == nil || *reordered[0].Score != 0.99 {
		t.Fatalf("unexpected score = %v", reordered[0].Score)
	}
}
