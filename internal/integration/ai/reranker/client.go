package reranker

import usecasereranker "assistant-api/internal/usecase/ai/reranker"

// RankedDocument 表示 cross-encoder 重排後的單筆結果。
type RankedDocument = usecasereranker.RankedDocument

// Service 抽象出 cross-encoder rerank 能力。
type Service = usecasereranker.Service

// NewClient 由 AI integration 層建立 reranker client。
func NewClient(baseURL string, timeoutSeconds int, rerankPath string, maxAttempts int, retryBackoffMS int, aliveProbeIntervalMS int, aliveProbeTimeoutMS int, aliveSuccessTTLMS int, aliveFailureCooldownMS int) Service {
	return usecasereranker.NewClient(baseURL, timeoutSeconds, rerankPath, maxAttempts, retryBackoffMS, aliveProbeIntervalMS, aliveProbeTimeoutMS, aliveSuccessTTLMS, aliveFailureCooldownMS)
}
