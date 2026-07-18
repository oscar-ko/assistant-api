package realtime

import (
	"fmt"
	"strings"

	"assistant-api/internal/config"
)

// BuildClassifierFromConfig builds the realtime classifier from ai.classifier.
// classifier 和 embedding/reranker 一樣只透過 target 指向 provider profile；
// endpoint URL/path/timeout 由 llm_providers.<provider>.profiles.<profile> 管理。
func BuildClassifierFromConfig(cfg config.AIConfig) (Classifier, string, error) {
	classifierCfg := cfg.Classifier
	if !classifierCfg.Enabled {
		return nil, "", nil
	}
	target := strings.TrimSpace(classifierCfg.Target)
	if target == "" {
		return nil, "", fmt.Errorf("classifier target is required when ai.classifier.enabled is true")
	}
	_, classifierProfile, err := config.ResolveLocalProviderProfile(config.LLMProviders, target)
	if err != nil {
		return nil, "", fmt.Errorf("invalid ai.classifier.target: %w", err)
	}
	// timeout 放在 profile 層，讓同一 provider 底下不同本地服務可各自調整延遲限制。
	timeout := classifierProfile.TimeoutSeconds
	if timeout <= 0 {
		return nil, "", fmt.Errorf("classifier profile timeout_seconds is required when ai.classifier.enabled is true")
	}
	return NewLocalClassifierClient(classifierProfile.URL, timeout, classifierProfile.Path, classifierCfg.Labels), target, nil
}
