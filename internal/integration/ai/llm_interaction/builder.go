package llminteraction

import (
	"fmt"
	"strings"

	"assistant-api/internal/config"
)

const (
	providerLocal = "local"
)

type roleTarget struct {
	provider string
	profile  string
	isLocal  bool
}

type builtRoleClient struct {
	client InteractionClient
	label  string
}

type resolvedProviderProfile struct {
	providerKey        string
	url                string
	token              string
	headers            map[string]string
	model              string
	isLocal            bool
	timeoutSeconds     int
	actionDecisionPath string
	questionAnswerPath string
}

// BuildClientFromConfig 依目前設定建立單一可重用的 LLM interaction client。
// 所有通訊平台（LINE/Slack/WhatsApp）都應共用這條建構路徑，避免重複 wiring。
func BuildClientFromConfig(cfg config.AIConfig, llmProviders map[string]config.LLMProviderConfig) (InteractionClient, error) {
	decisionTarget, err := parseRoleTarget(strings.TrimSpace(cfg.LLMInteraction.Decision.Profile))
	if err != nil {
		return nil, fmt.Errorf("invalid ai.llm_interaction.decision: %w", err)
	}
	chatTarget, err := parseRoleTarget(strings.TrimSpace(cfg.LLMInteraction.Chat.Profile))
	if err != nil {
		return nil, fmt.Errorf("invalid ai.llm_interaction.chat: %w", err)
	}

	decisionClient, err := buildRoleClient(cfg, llmProviders, decisionTarget, cfg.LLMInteraction.Decision)
	if err != nil {
		return nil, fmt.Errorf("failed to build decision client: %w", err)
	}
	chatClient, err := buildRoleClient(cfg, llmProviders, chatTarget, cfg.LLMInteraction.Chat)
	if err != nil {
		return nil, fmt.Errorf("failed to build chat client: %w", err)
	}

	return NewRoutedInteractionClient(decisionClient.client, decisionClient.label, chatClient.client, chatClient.label)
}

func parseRoleTarget(raw string) (roleTarget, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return roleTarget{}, fmt.Errorf("target is required")
	}
	if strings.EqualFold(trimmed, providerLocal) {
		return roleTarget{isLocal: true}, nil
	}
	parts := strings.Split(trimmed, ".")
	if len(parts) != 2 {
		return roleTarget{}, fmt.Errorf("target must be local or <provider>.<profile>")
	}
	provider := strings.TrimSpace(parts[0])
	profile := strings.TrimSpace(parts[1])
	if provider == "" || profile == "" {
		return roleTarget{}, fmt.Errorf("target must be local or <provider>.<profile>")
	}
	return roleTarget{provider: provider, profile: profile}, nil
}

func buildRoleClient(cfg config.AIConfig, llmProviders map[string]config.LLMProviderConfig, target roleTarget, options config.LLMRoleConfig) (*builtRoleClient, error) {
	if target.isLocal {
		serviceURL := strings.TrimSpace(cfg.LLMInteraction.Local.ServiceURL)
		timeout := cfg.LLMInteraction.Local.ServiceTimeoutSeconds
		if serviceURL == "" {
			return nil, fmt.Errorf("ai.llm_interaction.local.service_url is required when provider=local")
		}
		if timeout <= 0 {
			return nil, fmt.Errorf("ai.llm_interaction.local.service_timeout_seconds is required when provider=local")
		}
		client := NewInteractionClient(serviceURL, timeout)
		if client == nil {
			return nil, fmt.Errorf("failed to initialize local interaction client")
		}
		return &builtRoleClient{client: client, label: "local"}, nil
	}
	resolved, err := resolveProviderProfile(llmProviders, target.provider, target.profile)
	if err != nil {
		return nil, err
	}
	if resolved.isLocal {
		timeout := resolved.timeoutSeconds
		if options.Timeout > 0 {
			timeout = options.Timeout
		}
		client := NewLocalContractInteractionClient(
			resolved.url,
			timeout,
			resolved.actionDecisionPath,
			resolved.questionAnswerPath,
		)
		if client == nil {
			return nil, fmt.Errorf("failed to initialize local interaction client for provider %s", resolved.providerKey)
		}
		label := resolved.providerKey + "." + strings.TrimSpace(target.profile) + " local"
		return &builtRoleClient{client: client, label: label}, nil
	}
	if options.Timeout <= 0 {
		return nil, fmt.Errorf("llm_interaction cloud timeout_seconds is required for target %s.%s", resolved.providerKey, strings.TrimSpace(target.profile))
	}
	client, err := NewOpenAIInteractionClient(
		resolved.url,
		resolved.token,
		resolved.model,
		resolved.model,
		options.Timeout,
		options.MaxToken,
		options.Temperature,
	)
	if err != nil {
		return nil, err
	}
	label := resolved.providerKey + "." + strings.TrimSpace(target.profile) + " model=" + resolved.model
	return &builtRoleClient{client: client, label: label}, nil
}

func mergeProfile(profile config.LLMProfileConfig) (resolvedProviderProfile, error) {
	model := strings.TrimSpace(profile.ModelName)
	timeout := profile.TimeoutSeconds
	if model == "" && timeout <= 0 {
		return resolvedProviderProfile{}, fmt.Errorf("profile timeout_seconds is required")
	}

	return resolvedProviderProfile{
		model:              model,
		isLocal:            model == "",
		timeoutSeconds:     timeout,
		actionDecisionPath: strings.TrimSpace(profile.ActionDecisionPath),
		questionAnswerPath: strings.TrimSpace(profile.QuestionAnswerPath),
	}, nil
}

func resolveProviderProfile(llmProviders map[string]config.LLMProviderConfig, providerKey string, profileKey string) (resolvedProviderProfile, error) {
	providerKey = strings.TrimSpace(providerKey)
	profileKey = strings.TrimSpace(profileKey)
	if providerKey == "" || profileKey == "" {
		return resolvedProviderProfile{}, fmt.Errorf("provider/profile are required")
	}
	providers := llmProviders
	if providers == nil {
		providers = map[string]config.LLMProviderConfig{}
	}
	provider, ok := providers[providerKey]
	if !ok {
		return resolvedProviderProfile{}, fmt.Errorf("unknown provider: %s", providerKey)
	}
	profiles := provider.Profiles
	if profiles == nil {
		profiles = map[string]config.LLMProfileConfig{}
	}
	profile, ok := profiles[profileKey]
	if !ok {
		return resolvedProviderProfile{}, fmt.Errorf("unknown profile %s for provider %s", profileKey, providerKey)
	}
	resolved, err := mergeProfile(profile)
	if err != nil {
		return resolvedProviderProfile{}, fmt.Errorf("provider %s profile %s invalid: %w", providerKey, profileKey, err)
	}
	resolved.providerKey = providerKey
	resolved.url = strings.TrimSpace(profile.URL)
	if resolved.url == "" {
		resolved.url = strings.TrimSpace(provider.URL)
	}
	resolved.token = strings.TrimSpace(provider.Token)
	resolved.headers = provider.Headers
	if resolved.url == "" {
		return resolvedProviderProfile{}, fmt.Errorf("provider %s url is required", providerKey)
	}
	if !resolved.isLocal && resolved.token == "" {
		return resolvedProviderProfile{}, fmt.Errorf("provider %s token is required for cloud provider", providerKey)
	}
	return resolved, nil
}

// BuildServiceFromConfig 建立可跨平台共用的 LLM interaction service。
func BuildServiceFromConfig(cfg config.AIConfig, llmProviders map[string]config.LLMProviderConfig) (InteractionService, error) {
	client, err := BuildClientFromConfig(cfg, llmProviders)
	if err != nil {
		return nil, err
	}
	return NewInteractionService(client), nil
}
