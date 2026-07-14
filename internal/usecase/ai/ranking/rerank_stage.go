package ranking

import (
	"context"
	"strings"

	"assistant-api/internal/repository"
)

// TextPairReranker 抽象底層文字對重排能力。
// 這是模型/服務適配層需要實作的最小介面，避免 ranking 層綁定特定 provider。
type TextPairReranker interface {
	Rerank(ctx context.Context, query string, documents []string, topK int) ([]RankedDocument, error)
}

// RankedDocument 是文字對重排後的單筆輸出。
type RankedDocument struct {
	Index    int
	Document string
	Score    float64
}

// crossEncoderRerankStage 透過 cross-encoder 對候選做重排序。
type crossEncoderRerankStage struct {
	// ranker 為實際呼叫 cross-encoder 的抽象介面。
	ranker TextPairReranker
	// topK 控制希望服務端回傳的最大排序筆數。
	topK int
}

// NewCrossEncoderRerankStage 建立 rerank stage（屬於 ranking 流程層）。
func NewCrossEncoderRerankStage(ranker TextPairReranker, topK int) CandidateStage {
	return &crossEncoderRerankStage{ranker: ranker, topK: topK}
}

// Apply 只負責候選重排，不負責召回。
// 回傳值依序為：重排後候選(含分數)、是否真的有套用重排、錯誤。
func (s *crossEncoderRerankStage) Apply(ctx context.Context, query string, candidates []repository.ActionRouteVectorCandidate) ([]ScoredCandidate, bool, error) {
	// 候選不足 2 筆時重排沒有意義，直接略過。
	if len(candidates) <= 1 || s == nil || s.ranker == nil {
		return buildScoredCandidates(candidates, nil), false, nil
	}

	type rerankInput struct {
		// sourceIndex 紀錄原候選索引，重排後可映射回完整 candidate。
		sourceIndex int
		// document 為送進 cross-encoder 的文本。
		document string
	}
	// 先整理可重排的輸入，並保留索引對照表。
	inputs := make([]rerankInput, 0, len(candidates))
	documents := make([]string, 0, len(candidates))
	for idx, candidate := range candidates {
		// 優先使用 route_text；若缺失則退回 API operation。
		doc := strings.TrimSpace(candidate.RouteText)
		if doc == "" {
			doc = strings.TrimSpace(candidate.APIOperation)
		}
		// 仍為空代表該候選無可排序文本，直接略過。
		if doc == "" {
			continue
		}
		inputs = append(inputs, rerankInput{sourceIndex: idx, document: doc})
		documents = append(documents, doc)
	}

	// 最少需兩筆文本才值得送 cross-encoder 計算相對排序。
	if len(documents) <= 1 {
		return buildScoredCandidates(candidates, nil), false, nil
	}

	// 呼叫模型服務取得新順序與分數。
	rankedDocs, err := s.ranker.Rerank(ctx, strings.TrimSpace(query), documents, s.topK)
	if err != nil {
		return buildScoredCandidates(candidates, nil), false, err
	}
	// 若服務端回空，視為未套用重排而非異常。
	if len(rankedDocs) == 0 {
		return buildScoredCandidates(candidates, nil), false, nil
	}

	// 依服務端回傳索引重建候選順序，並同步收集分數。
	reordered := make([]repository.ActionRouteVectorCandidate, 0, len(rankedDocs))
	scores := make([]float64, 0, len(rankedDocs))
	for _, ranked := range rankedDocs {
		// 防禦式檢查：避免外部服務回傳越界索引造成 panic。
		if ranked.Index < 0 || ranked.Index >= len(inputs) {
			continue
		}
		sourceIndex := inputs[ranked.Index].sourceIndex
		// 再次檢查原候選索引合法性。
		if sourceIndex < 0 || sourceIndex >= len(candidates) {
			continue
		}
		reordered = append(reordered, candidates[sourceIndex])
		scores = append(scores, ranked.Score)
	}
	// 全數索引無效時回退原排序，避免輸出空候選。
	if len(reordered) == 0 {
		return buildScoredCandidates(candidates, nil), false, nil
	}

	return buildScoredCandidates(reordered, scores), true, nil
}
