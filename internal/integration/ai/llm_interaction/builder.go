package llminteraction

import (
	"fmt"
	"strings"

	"assistant-api/internal/config"
)

const (
	providerLocal   = "local"
	providerChatGPT = "chatgpt"
)

type roleTarget struct {
	provider string
	profile  string
}

type builtRoleClient struct {
	client InteractionClient
	label  string
}

type chatGPTResolvedModel struct {
	model          string
	timeoutSeconds int
	maxTokens      *int
	temperature    *float64
}

// BuildClientFromConfig 依目前設定建立單一可重用的 LLM interaction client。
// 所有通訊平台（LINE/Slack/WhatsApp）都應共用這條建構路徑，避免重複 wiring。
func BuildClientFromConfig(cfg config.LLMInteractionConfig) (InteractionClient, error) {
	decisionTarget, err := parseRoleTarget(strings.TrimSpace(cfg.Decision))
	if err != nil {
		return nil, fmt.Errorf("invalid ai.llm_interaction.decision: %w", err)
	}
	chatTarget, err := parseRoleTarget(strings.TrimSpace(cfg.Chat))
	if err != nil {
		return nil, fmt.Errorf("invalid ai.llm_interaction.chat: %w", err)
	}

	decisionClient, err := buildRoleClient(cfg, decisionTarget)
	if err != nil {
		return nil, fmt.Errorf("failed to build decision client: %w", err)
	}
	chatClient, err := buildRoleClient(cfg, chatTarget)
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
		return roleTarget{provider: providerLocal}, nil
	}
	// decision/chat 非 local 時，一律視為 chatgpt profile key。
	return roleTarget{provider: providerChatGPT, profile: trimmed}, nil
}

func buildRoleClient(cfg config.LLMInteractionConfig, target roleTarget) (*builtRoleClient, error) {
	switch strings.ToLower(strings.TrimSpace(target.provider)) {
	case providerLocal:
		serviceURL := strings.TrimSpace(cfg.Local.ServiceURL)
		timeout := cfg.Local.ServiceTimeoutSeconds
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
	case providerChatGPT:
		resolved, err := resolveChatGPTProfile(cfg.ChatGPT, strings.TrimSpace(target.profile))
		if err != nil {
			return nil, err
		}
		client, err := NewOpenAIInteractionClient(
			cfg.ChatGPT.URL,
			cfg.ChatGPT.Token,
			resolved.model,
			resolved.model,
			resolved.timeoutSeconds,
			resolved.maxTokens,
			resolved.temperature,
		)
		if err != nil {
			return nil, err
		}
		return &builtRoleClient{client: client, label: "chatgpt:" + strings.TrimSpace(target.profile) + " model=" + resolved.model}, nil
	default:
		return nil, fmt.Errorf("unsupported provider: %s", strings.TrimSpace(target.provider))
	}
}

func resolveChatGPTProfile(cfg config.ChatGPTConfig, profileKey string) (chatGPTResolvedModel, error) {
	profileKey = strings.TrimSpace(profileKey)
	profiles := cfg.Profiles
	if profiles == nil {
		profiles = map[string]config.ChatGPTModelConfig{}
	}
	if profileKey == "" {
		return chatGPTResolvedModel{}, fmt.Errorf("chatgpt profile is required")
	}
	profile, ok := profiles[profileKey]
	if !ok {
		return chatGPTResolvedModel{}, fmt.Errorf("unknown chatgpt profile: %s", profileKey)
	}

	return mergeChatGPTModel(profile, cfg)
}

func mergeChatGPTModel(profile config.ChatGPTModelConfig, root config.ChatGPTConfig) (chatGPTResolvedModel, error) {
	model := strings.TrimSpace(profile.ModelName)
	if model == "" {
		return chatGPTResolvedModel{}, fmt.Errorf("chatgpt model is required")
	}

	timeout := profile.TimeoutSeconds
	if timeout <= 0 {
		return chatGPTResolvedModel{}, fmt.Errorf("chatgpt profile timeout_seconds is required")
	}

	var maxTokens *int
	if profile.MaxTokens != nil {
		value := *profile.MaxTokens
		if value > 0 {
			maxTokens = &value
		}
	}

	var temperature *float64
	if profile.Temperature != nil {
		value := *profile.Temperature
		temperature = &value
	}

	return chatGPTResolvedModel{
		model:          model,
		timeoutSeconds: timeout,
		maxTokens:      maxTokens,
		temperature:    temperature,
	}, nil
}

// BuildServiceFromConfig 建立可跨平台共用的 LLM interaction service。
func BuildServiceFromConfig(cfg config.LLMInteractionConfig) (InteractionService, error) {
	client, err := BuildClientFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	return NewInteractionService(client), nil
}
