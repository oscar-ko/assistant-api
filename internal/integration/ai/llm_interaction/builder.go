package llminteraction

import (
	"fmt"
	"strings"

	"assistant-api/internal/config"
)

type roleTarget struct {
	provider string
	profile  string
}

type builtRoleClient struct {
	client InteractionClient
	label  string
}

type resolvedProviderProfile struct {
	providerKey    string
	providerType   string
	url            string
	token          string
	headers        map[string]string
	model          string
	isLocal        bool
	timeoutSeconds int
	// useJSONResponseFmt 完全由設定檔控制，不應用 model 名稱或 profile 名稱硬編碼判斷。
	useJSONResponseFmt *bool
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
	parts := strings.Split(trimmed, ".")
	if len(parts) != 2 {
		return roleTarget{}, fmt.Errorf("target must be <provider>.<profile>")
	}
	provider := strings.TrimSpace(parts[0])
	profile := strings.TrimSpace(parts[1])
	if provider == "" || profile == "" {
		return roleTarget{}, fmt.Errorf("target must be <provider>.<profile>")
	}
	return roleTarget{provider: provider, profile: profile}, nil
}

func buildRoleClient(cfg config.AIConfig, llmProviders map[string]config.LLMProviderConfig, target roleTarget, options config.LLMRoleConfig) (*builtRoleClient, error) {
	resolved, err := resolveProviderProfile(llmProviders, target.provider, target.profile)
	if err != nil {
		return nil, err
	}
	if options.Timeout <= 0 {
		return nil, fmt.Errorf("llm_interaction cloud timeout_seconds is required for target %s.%s", resolved.providerKey, strings.TrimSpace(target.profile))
	}
	if resolved.isLocal {
		// local provider 不直接啟動不同 port 的模型服務；同一個 9003 服務依 request.model_name 切換模型。
		// 因此 profile.model_name 在這裡不是雲端 API model，而是傳給 9003 的 Ollama model selector。
		client := NewLocalContractInteractionClientWithModel(
			resolved.url,
			options.Timeout,
			resolved.model,
			resolved.actionDecisionPath,
			resolved.questionAnswerPath,
		)
		if client == nil {
			return nil, fmt.Errorf("failed to initialize local interaction client for provider %s", resolved.providerKey)
		}
		label := resolved.providerKey + "." + strings.TrimSpace(target.profile) + " local"
		// log label 保留 model 名稱，方便排查 action decision 走 9B、todo extractor 走 2B 時的實際路由。
		if strings.TrimSpace(resolved.model) != "" {
			label += " model=" + strings.TrimSpace(resolved.model)
		}
		return &builtRoleClient{client: client, label: label}, nil
	}
	client, err := NewOpenAIInteractionClient(
		resolved.url,
		resolved.token,
		resolved.model,
		resolved.model,
		options.Timeout,
		options.MaxToken,
		options.Temperature,
		resolved.useJSONResponseFmt,
	)
	if err != nil {
		return nil, err
	}
	label := resolved.providerKey + "." + strings.TrimSpace(target.profile) + " model=" + resolved.model
	return &builtRoleClient{client: client, label: label}, nil
}

func mergeProvider(provider config.LLMProviderConfig) (resolvedProviderProfile, error) {
	providerType := strings.ToLower(strings.TrimSpace(provider.Type))
	if providerType != "local" && providerType != "cloud" {
		return resolvedProviderProfile{}, fmt.Errorf("provider type is required and must be local or cloud")
	}
	return resolvedProviderProfile{providerType: providerType}, nil
}

func mergeProfile(profile config.LLMProfileConfig, providerType string) (resolvedProviderProfile, error) {
	model := strings.TrimSpace(profile.ModelName)
	if providerType == "cloud" && model == "" {
		return resolvedProviderProfile{}, fmt.Errorf("cloud profile model_name is required")
	}

	return resolvedProviderProfile{
		model:              model,
		timeoutSeconds:     profile.TimeoutSeconds,
		useJSONResponseFmt: profile.UseJSONResponseFmt,
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
	providerMeta, err := mergeProvider(provider)
	if err != nil {
		return resolvedProviderProfile{}, fmt.Errorf("provider %s invalid: %w", providerKey, err)
	}
	resolved, err := mergeProfile(profile, providerMeta.providerType)
	if err != nil {
		return resolvedProviderProfile{}, fmt.Errorf("provider %s profile %s invalid: %w", providerKey, profileKey, err)
	}
	resolved.providerKey = providerKey
	resolved.providerType = providerMeta.providerType
	resolved.url = strings.TrimSpace(profile.URL)
	if resolved.url == "" {
		resolved.url = strings.TrimSpace(provider.URL)
	}
	resolved.token = strings.TrimSpace(provider.Token)
	resolved.headers = provider.Headers
	if resolved.url == "" {
		return resolvedProviderProfile{}, fmt.Errorf("provider %s url is required", providerKey)
	}
	resolved.isLocal = resolved.providerType == "local"
	if resolved.providerType == "cloud" && resolved.token == "" {
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
