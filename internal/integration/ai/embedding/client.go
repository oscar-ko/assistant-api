package embedding

import usecaseembedding "assistant-api/internal/usecase/ai/embedding"

// Service 轉換文字為 embedding 向量。
type Service = usecaseembedding.Service

// NewClient 由 AI integration 層建立 embedding client。
func NewClient(baseURL string, timeoutSeconds int, embedPath string, maxAttempts int, retryBackoffMS int, aliveProbeIntervalMS int, aliveProbeTimeoutMS int, aliveSuccessTTLMS int, aliveFailureCooldownMS int) Service {
	return usecaseembedding.NewClient(baseURL, timeoutSeconds, embedPath, maxAttempts, retryBackoffMS, aliveProbeIntervalMS, aliveProbeTimeoutMS, aliveSuccessTTLMS, aliveFailureCooldownMS)
}
