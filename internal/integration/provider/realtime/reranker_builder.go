package realtime

import (
	"fmt"
	"strings"

	"assistant-api/internal/config"
	aireranker "assistant-api/internal/integration/ai/reranker"
	usecasereranker "assistant-api/internal/usecase/ai/reranker"
)

// BuildRerankerFromConfig 依 ai.reranker 設定建立可選的 cross-encoder reranker。
//
// 這個 builder 讓 realtime services 可共用同一個 reranker 設定來源，
// 避免 LINE/Slack 各自解析 provider profile 造成接線分歧。
//
// 設計細節：
//  1. ai.reranker.enabled=false 時回傳 nil service，表示「目前不做第二階段精排」，
//     呼叫端仍可用時間序或原候選序繼續處理，不需要知道設定檔格式。
//  2. target 必須指向 llm_providers 底下的 local profile；URL/path/model 等 endpoint 細節
//     由 profile 管理，避免把本機服務位址散落在各個 provider webhook 裡。
//  3. 這裡只負責 transport client 建構，不負責決定哪些訊息要 rerank；
//     「是否需要 rerank」仍由各 realtime usecase 依自己的語意與成本判斷。
func BuildRerankerFromConfig(cfg config.AIConfig) (usecasereranker.Service, string, error) {
	rerankerCfg := cfg.Reranker
	if !rerankerCfg.Enabled {
		// nil service 是明確的「未啟用」狀態，讓上層可用依賴注入判斷，而不是讀全域 config。
		return nil, "", nil
	}
	target := strings.TrimSpace(rerankerCfg.Target)
	if target == "" {
		return nil, "", fmt.Errorf("reranker target is required when ai.reranker.enabled is true")
	}
	_, rerankerProfile, err := config.ResolveLocalProviderProfile(config.LLMProviders, target)
	if err != nil {
		return nil, "", fmt.Errorf("invalid ai.reranker.target: %w", err)
	}
	// timeout/retry/alive probe 仍沿用 ai.reranker 的服務級設定；profile 只提供 endpoint 契約，
	// 這樣同一個 reranker profile 在不同環境可透過 ai.reranker 調整可靠性參數。
	rerankerClient := aireranker.NewClient(
		rerankerProfile.URL,
		rerankerCfg.TimeoutSeconds,
		rerankerProfile.Path,
		rerankerCfg.MaxAttempts,
		rerankerCfg.RetryBackoffMS,
		rerankerCfg.AliveProbeIntervalMS,
		rerankerCfg.AliveProbeTimeoutMS,
		rerankerCfg.AliveSuccessTTLMS,
		rerankerCfg.AliveFailureCooldownMS,
	)
	if rerankerClient == nil {
		return nil, "", fmt.Errorf("failed to initialize reranker client")
	}
	return rerankerClient, target, nil
}
