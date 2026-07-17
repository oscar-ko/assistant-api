package realtime

import (
	"fmt"
	"strings"

	"assistant-api/internal/config"
)

// BuildTranslatorFromConfig 依設定建立翻譯實作。
//
// 規則：
// - 使用 <provider>.<profile>，由 llm_providers.<provider>.profiles.<profile> 決定實作
func BuildTranslatorFromConfig(cfg config.AIConfig, llmProviders map[string]config.LLMProviderConfig) (Translator, string, error) {
	return BuildTranslatorFromTarget(cfg, llmProviders, strings.TrimSpace(cfg.LLMInteraction.Translate.Profile))
}

// BuildTranslatorFromTarget 依指定 target 建立翻譯實作。
//
// 這個入口給「單次指令翻譯」與「即時翻譯」共用：
// 呼叫端可自行決定要用哪個 target（例如 openai.chat_search、openrouter.nothappy_chat）。
func BuildTranslatorFromTarget(cfg config.AIConfig, llmProviders map[string]config.LLMProviderConfig, target string) (Translator, string, error) {
	target = strings.TrimSpace(target)
	if target == "" {
		return nil, "", fmt.Errorf("translate target is required")
	}

	providerKey, profileKey, err := parseProviderProfileTarget(target)
	if err != nil {
		return nil, "", err
	}
	provider, profile, err := resolveProviderProfile(llmProviders, providerKey, profileKey)
	if err != nil {
		return nil, "", err
	}
	providerType := strings.ToLower(strings.TrimSpace(provider.Type))
	if providerType == "local" {
		url := strings.TrimSpace(profile.URL)
		if url == "" {
			url = strings.TrimSpace(provider.URL)
		}
		if url == "" {
			return nil, "", fmt.Errorf("provider %s requires url", providerKey)
		}
		timeout := profile.TimeoutSeconds
		if cfg.LLMInteraction.Translate.Timeout > 0 {
			timeout = cfg.LLMInteraction.Translate.Timeout
		}
		if timeout <= 0 {
			return nil, "", fmt.Errorf("provider %s profile %s requires timeout_seconds", providerKey, profileKey)
		}
		translatePath := strings.TrimSpace(profile.TranslatePath)
		if translatePath == "" {
			translatePath = strings.TrimSpace(profile.Path)
		}
		return NewLocalContractTranslateClient(url, timeout, translatePath), target, nil
	}
	if providerType != "cloud" {
		return nil, "", fmt.Errorf("provider %s type must be local or cloud", providerKey)
	}
	{
		url := strings.TrimSpace(profile.URL)
		if url == "" {
			url = strings.TrimSpace(provider.URL)
		}
		if cfg.LLMInteraction.Translate.Timeout <= 0 {
			return nil, "", fmt.Errorf("llm_interaction translate_options.timeout_seconds is required for cloud target %s.%s", providerKey, profileKey)
		}
		client, err := NewCloudTranslateClient(
			url,
			provider.Token,
			profile.ModelName,
			cfg.LLMInteraction.Translate.Timeout,
			cfg.LLMInteraction.Translate.MaxToken,
			cfg.LLMInteraction.Translate.Temperature,
			provider.Headers,
		)
		if err != nil {
			return nil, "", fmt.Errorf("provider %s profile %s init failed: %w", providerKey, profileKey, err)
		}
		return client, target, nil
	}
}

func parseProviderProfileTarget(target string) (string, string, error) {
	target = strings.TrimSpace(target)
	parts := strings.Split(target, ".")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("translate target must be <provider>.<profile>")
	}
	provider := strings.TrimSpace(parts[0])
	profile := strings.TrimSpace(parts[1])
	if provider == "" || profile == "" {
		return "", "", fmt.Errorf("translate target must be <provider>.<profile>")
	}
	return provider, profile, nil
}

func resolveProviderProfile(llmProviders map[string]config.LLMProviderConfig, providerKey string, profileKey string) (config.LLMProviderConfig, config.LLMProfileConfig, error) {
	providers := llmProviders
	if providers == nil {
		providers = map[string]config.LLMProviderConfig{}
	}
	provider, ok := providers[providerKey]
	if !ok {
		return config.LLMProviderConfig{}, config.LLMProfileConfig{}, fmt.Errorf("unknown provider: %s", providerKey)
	}
	profiles := provider.Profiles
	if profiles == nil {
		profiles = map[string]config.LLMProfileConfig{}
	}
	profile, ok := profiles[profileKey]
	if !ok {
		return config.LLMProviderConfig{}, config.LLMProfileConfig{}, fmt.Errorf("unknown profile %s for provider %s", profileKey, providerKey)
	}
	return provider, profile, nil
}
