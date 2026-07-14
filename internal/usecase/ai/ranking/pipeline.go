package ranking

import (
	"context"

	"assistant-api/internal/repository"
)

// CandidateRetriever 抽象候選召回能力。
// 實作可來自向量召回、關鍵字召回或其他 retrieval 策略。
type CandidateRetriever interface {
	Retrieve(ctx context.Context, query string) ([]repository.ActionRouteVectorCandidate, error)
}

// ScoredCandidate 將候選與分數綁在同一筆資料，避免平行陣列索引錯位。
// Score 為 nil 代表該 stage 沒有提供分數（例如僅做過濾）。
type ScoredCandidate struct {
	Candidate repository.ActionRouteVectorCandidate
	Score     *float64
}

// CandidatePipeline 定義高層候選組合介面：
// - RetrieveCandidates: 只做第一階段候選召回
// - RerankCandidates: 對既有候選套用後處理 stages
// 呼叫端可只用其中一段，也可完整串接。
type CandidatePipeline interface {
	RetrieveCandidates(ctx context.Context, query string) ([]repository.ActionRouteVectorCandidate, error)
	RerankCandidates(ctx context.Context, query string, candidates []repository.ActionRouteVectorCandidate) ([]ScoredCandidate, bool, error)
}

// CandidateStage 定義可插拔的候選處理階段。
// 可用於 rerank，也可擴充成 threshold、policy、dedup 等後處理。
type CandidateStage interface {
	Apply(ctx context.Context, query string, candidates []repository.ActionRouteVectorCandidate) ([]ScoredCandidate, bool, error)
}

type candidatePipeline struct {
	// retriever 是第一階段召回器，負責把 query 轉成候選集合。
	retriever CandidateRetriever
	// stages 是第二階段處理鏈，依序對候選做重排/過濾/補分等操作。
	stages []CandidateStage
}

// NewCandidatePipelineWithStages 允許呼叫端自訂 stage 順序與組合。
func NewCandidatePipelineWithStages(retriever CandidateRetriever, stages ...CandidateStage) CandidatePipeline {
	if retriever == nil {
		return nil
	}
	// 過濾掉 nil stage，避免執行時發生 nil dereference。
	filteredStages := make([]CandidateStage, 0, len(stages))
	for _, stage := range stages {
		if stage == nil {
			continue
		}
		filteredStages = append(filteredStages, stage)
	}
	return &candidatePipeline{retriever: retriever, stages: filteredStages}
}

func (p *candidatePipeline) RetrieveCandidates(ctx context.Context, query string) ([]repository.ActionRouteVectorCandidate, error) {
	// 若 pipeline 未初始化，視為無候選而非錯誤，避免中斷上游流程。
	if p == nil || p.retriever == nil {
		return nil, nil
	}
	return p.retriever.Retrieve(ctx, query)
}

func (p *candidatePipeline) RerankCandidates(ctx context.Context, query string, candidates []repository.ActionRouteVectorCandidate) ([]ScoredCandidate, bool, error) {
	// 沒有任何 stage 時，直接沿用召回結果，並標記未套用重排。
	if p == nil || len(p.stages) == 0 {
		return buildScoredCandidates(candidates, nil), false, nil
	}

	// current 會被每個 stage 覆寫，代表「目前版本的候選集合」。
	current := candidates
	lastScored := buildScoredCandidates(candidates, nil)
	appliedAny := false
	for _, stage := range p.stages {
		// 逐段執行 stage，確保順序可預期且可擴充。
		nextScored, applied, err := stage.Apply(ctx, query, current)
		if err != nil {
			// 若某段失敗，回傳上一個穩定版本，避免輸出半套資料。
			return lastScored, appliedAny, err
		}
		lastScored = nextScored
		current = extractCandidates(nextScored)
		if applied {
			appliedAny = true
		}
	}

	return lastScored, appliedAny, nil
}

// buildScoredCandidates 依候選與分數組裝顯式結構，避免上游自行對位。
func buildScoredCandidates(candidates []repository.ActionRouteVectorCandidate, scores []float64) []ScoredCandidate {
	out := make([]ScoredCandidate, 0, len(candidates))
	for idx, candidate := range candidates {
		var scorePtr *float64
		if idx < len(scores) {
			scoreCopy := scores[idx]
			scorePtr = &scoreCopy
		}
		out = append(out, ScoredCandidate{Candidate: candidate, Score: scorePtr})
	}
	return out
}

// extractCandidates 將 stage 輸出還原為純候選集合，供下一段 stage 使用。
func extractCandidates(items []ScoredCandidate) []repository.ActionRouteVectorCandidate {
	out := make([]repository.ActionRouteVectorCandidate, 0, len(items))
	for _, item := range items {
		out = append(out, item.Candidate)
	}
	return out
}
