package topkfilter

import (
	"fmt"

	"assistant-api/internal/config"
	aiembedding "assistant-api/internal/integration/ai/embedding"
	aireranker "assistant-api/internal/integration/ai/reranker"
	usecasetopkfilter "assistant-api/internal/usecase/ai/topkfilter"
)

const defaultLocale = "zh-TW"

// Service 對外提供與 usecase 相同的 top-k filter service 介面。
type Service = usecasetopkfilter.Service

// BuildServiceFromConfig 由集中設定一次組裝 embedding + reranker + top-k filter。
// 所有通道（LINE/Slack/WhatsApp）都應共用這個建構入口，避免重複 wiring。
func BuildServiceFromConfig(searcher usecasetopkfilter.Searcher, cfg config.AIConfig) (Service, error) {
	_, embeddingProfile, err := config.ResolveLocalProviderProfile(config.LLMProviders, cfg.Embedding.Target)
	if err != nil {
		return nil, fmt.Errorf("invalid ai.embedding.target: %w", err)
	}
	embeddingClient := aiembedding.NewClient(
		embeddingProfile.URL,
		cfg.Embedding.TimeoutSeconds,
		embeddingProfile.Path,
		cfg.Embedding.MaxAttempts,
		cfg.Embedding.RetryBackoffMS,
		cfg.Embedding.AliveProbeIntervalMS,
		cfg.Embedding.AliveProbeTimeoutMS,
		cfg.Embedding.AliveSuccessTTLMS,
		cfg.Embedding.AliveFailureCooldownMS,
	)
	if embeddingClient == nil {
		return nil, fmt.Errorf("failed to initialize embedding client")
	}

	if cfg.Reranker.Enabled {
		_, rerankerProfile, err := config.ResolveLocalProviderProfile(config.LLMProviders, cfg.Reranker.Target)
		if err != nil {
			return nil, fmt.Errorf("invalid ai.reranker.target: %w", err)
		}
		rerankerClient := aireranker.NewClient(
			rerankerProfile.URL,
			cfg.Reranker.TimeoutSeconds,
			rerankerProfile.Path,
			cfg.Reranker.MaxAttempts,
			cfg.Reranker.RetryBackoffMS,
			cfg.Reranker.AliveProbeIntervalMS,
			cfg.Reranker.AliveProbeTimeoutMS,
			cfg.Reranker.AliveSuccessTTLMS,
			cfg.Reranker.AliveFailureCooldownMS,
		)
		if rerankerClient == nil {
			return nil, fmt.Errorf("failed to initialize reranker client")
		}

		svc := usecasetopkfilter.NewServiceWithReranker(
			searcher,
			embeddingClient,
			rerankerClient,
			defaultLocale,
			cfg.Embedding.RetrievalTopK,
			cfg.Reranker.TopK,
		)
		if svc == nil {
			return nil, fmt.Errorf("failed to initialize top-k filter service with reranker")
		}
		return svc, nil
	}

	svc := usecasetopkfilter.NewService(searcher, embeddingClient, defaultLocale, cfg.Embedding.RetrievalTopK)
	if svc == nil {
		return nil, fmt.Errorf("failed to initialize top-k filter service")
	}
	return svc, nil
}
