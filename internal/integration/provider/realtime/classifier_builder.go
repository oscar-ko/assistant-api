package realtime

import (
	"fmt"
	"strings"

	"assistant-api/internal/config"
)

// BuildClassifierFromConfig builds the realtime classifier from ai.classifier.
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
	timeout := classifierProfile.TimeoutSeconds
	if timeout <= 0 {
		return nil, "", fmt.Errorf("classifier profile timeout_seconds is required when ai.classifier.enabled is true")
	}
	return NewLocalClassifierClient(classifierProfile.URL, timeout, classifierProfile.Path, classifierCfg.Labels), target, nil
}
