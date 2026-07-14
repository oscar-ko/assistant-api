package topkfilter

import (
	"strings"

	"assistant-api/internal/usecase/ai/embedding"
	"assistant-api/internal/usecase/ai/ranking"
	"assistant-api/internal/usecase/ai/reranker"
)

// CandidatePipeline/CandidateStage 改由共用套件 ranking 提供。
// 這裡以 type alias 保持 topkfilter 現有呼叫端相容。
type CandidatePipeline = ranking.CandidatePipeline
type CandidateStage = ranking.CandidateStage
type ScoredCandidate = ranking.ScoredCandidate

// NewTopKRetriever 建立可獨立重用的 pure top-k 模組。
func NewTopKRetriever(searcher Searcher, embedder embedding.Service, locale string, topK int) TopKRetriever {
	// 召回依賴缺一不可，避免組出不可用 stage。
	if searcher == nil || embedder == nil {
		return nil
	}
	// locale 空值時回到預設語系，避免查詢 miss。
	locale = strings.TrimSpace(locale)
	if locale == "" {
		locale = defaultLocale
	}
	// topK 非法時採預設值，避免回傳 0 筆候選。
	if topK <= 0 {
		topK = defaultTopK
	}
	return newVectorRetriever(searcher, embedder, locale, topK)
}

// NewCandidateReranker 建立可獨立重用的 pure rerank stage。
func NewCandidateReranker(rerankerSvc reranker.Service, topK int) CandidateStage {
	// 由於 reranker 服務端可自行 fallback，這裡只保證 topK 正值。
	if topK <= 0 {
		topK = defaultTopK
	}
	return newCrossEncoderReranker(rerankerSvc, topK)
}

// NewCandidatePipelineWithStages 允許呼叫端自訂 stage 順序與組合。
// 這是未來擴充更多候選處理邏輯的主要入口。
func NewCandidatePipelineWithStages(retriever TopKRetriever, stages ...CandidateStage) CandidatePipeline {
	// 讓外部可以注入自訂 stage（例如 policy/threshold）。
	return ranking.NewCandidatePipelineWithStages(retriever, stages...)
}
