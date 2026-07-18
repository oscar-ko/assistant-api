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
	baseURL := strings.TrimSpace(classifierCfg.URL)
	if baseURL == "" {
		return nil, "", fmt.Errorf("classifier url is required when ai.classifier.enabled is true")
	}
	timeout := classifierCfg.TimeoutSeconds
	if timeout <= 0 {
		return nil, "", fmt.Errorf("classifier timeout_seconds is required when ai.classifier.enabled is true")
	}
	return NewLocalClassifierClient(baseURL, timeout, classifierCfg.Path, classifierCfg.Labels), baseURL, nil
}