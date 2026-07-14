package topkfilter

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/ai/embedding"
)

var (
	// 這些 sentinel error 讓 orchestrator 能用 errors.Is 判斷失敗類型，
	// 並輸出一致的 reason 欄位，不需要依賴字串比對。
	errEmbeddingFailed          = fmt.Errorf("embedding_failed")
	errMarshalQueryVectorFailed = fmt.Errorf("marshal_query_vector_failed")
	errVectorSearchFailed       = fmt.Errorf("vector_search_failed")
)

// TopKRetriever 抽象第一階段候選召回能力（pure top-k stage）。
type TopKRetriever interface {
	Retrieve(ctx context.Context, text string) ([]repository.ActionRouteVectorCandidate, error)
}

// vectorRetriever 封裝「text -> embedding -> pgvector search」流程。
// 這個 stage 的責任只有「召回候選」，不做任何排序重排邏輯。
type vectorRetriever struct {
	searcher Searcher
	embedder embedding.Service
	locale   string
	topK     int
}

// newVectorRetriever 建立 retrieval stage。
func newVectorRetriever(searcher Searcher, embedder embedding.Service, locale string, topK int) TopKRetriever {
	// 建構時不做參數修正，呼叫端（NewTopKRetriever）已保證 locale/topK 合法。
	return &vectorRetriever{searcher: searcher, embedder: embedder, locale: locale, topK: topK}
}

// Retrieve 執行第一階段召回：先 embedding，再做向量查詢拿候選。
// 這裡故意維持最小決策：
// - 不做 threshold 裁切
// - 不做 rerank
// - 不做業務規則修正
// 讓第二階段 rerank 與上游 orchestrator 保持可替換性。
func (r *vectorRetriever) Retrieve(ctx context.Context, text string) ([]repository.ActionRouteVectorCandidate, error) {
	// 初始化檢查放在入口，避免後面呼叫 nil interface 造成 panic。
	if r == nil || r.searcher == nil || r.embedder == nil {
		return nil, fmt.Errorf("vector retriever is not initialized")
	}

	// 文字轉向量失敗時，用 wrapped sentinel error 回報給 orchestrator。
	vector, err := r.embedder.GetEmbedding(ctx, strings.TrimSpace(text))
	if err != nil {
		// 透過 `%w` 包裝 sentinel error，讓上層可用 errors.Is 分流。
		return nil, fmt.Errorf("%w: %v", errEmbeddingFailed, err)
	}

	// repository 查詢介面目前吃 JSON vector 字串，因此先做 marshal。
	queryVector, err := json.Marshal(vector)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMarshalQueryVectorFailed, err)
	}

	// 目前 skillCodes 保持 nil，讓資料庫先回傳純距離排序候選。
	// 若未來要做 domain/intent 範圍限制，應由 orchestrator 傳入策略，
	// 而不是在 retrieval stage 內硬編碼規則。
	candidates, err := r.searcher.SearchTopByVectorAndLocale(ctx, r.locale, string(queryVector), r.topK, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errVectorSearchFailed, err)
	}

	// retrieval stage 只保證召回結果，不改動原始距離與順序語意。
	return candidates, nil
}
