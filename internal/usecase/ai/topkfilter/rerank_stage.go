package topkfilter

import (
	"context"

	"assistant-api/internal/usecase/ai/ranking"
	"assistant-api/internal/usecase/ai/reranker"
)

// textPairRerankerAdapter 把 provider 層回傳型別轉成 ranking 層型別。
// 這樣可維持分層：topkfilter 不直接把 reranker DTO 洩漏到 ranking。
type textPairRerankerAdapter struct {
	ranker reranker.Service
}

// newCrossEncoderReranker 建立 rerank stage。
func newCrossEncoderReranker(ranker reranker.Service, topK int) CandidateStage {
	return ranking.NewCrossEncoderRerankStage(textPairRerankerAdapter{ranker: ranker}, topK)
}

func (a textPairRerankerAdapter) Rerank(ctx context.Context, query string, documents []string, topK int) ([]ranking.RankedDocument, error) {
	// 未注入 reranker 時回傳 nil，讓上游自然走「不套用重排」分支。
	if a.ranker == nil {
		return nil, nil
	}
	// 先透過 provider 層介面呼叫實際服務。
	items, err := a.ranker.Rerank(ctx, query, documents, topK)
	if err != nil {
		return nil, err
	}
	// 明確做 DTO 轉換，避免跨層型別耦合。
	out := make([]ranking.RankedDocument, 0, len(items))
	for _, item := range items {
		out = append(out, ranking.RankedDocument{Index: item.Index, Document: item.Document, Score: item.Score})
	}
	return out, nil
}
