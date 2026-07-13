package commanddecision

import (
	"context"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/usecase/ai/semanticdecision"
	"assistant-api/internal/usecase/inbound/commandchain"
)

// Decision 表示單則訊息的共用判斷結果。
type Decision struct {
	MentionedBot          bool
	OnCommandChain        bool
	EffectiveMentionedBot bool
	Classification        *semanticdecision.Classification
	CommandChainError     error
	ClassificationError   error
}

// Service 封裝跨平台共用的指令判斷流程。
type Service interface {
	DecideMessage(ctx context.Context, message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, botUserID string) *Decision
}

type service struct {
	commandChain    commandchain.Service
	semanticService semanticdecision.Service
}

// NewService 建立共用指令判斷服務。
func NewService(commandChain commandchain.Service, semanticService semanticdecision.Service) Service {
	if commandChain == nil && semanticService == nil {
		return nil
	}
	return &service{commandChain: commandChain, semanticService: semanticService}
}

// DecideMessage 依序執行 mention、訊息鍊與語意分類，輸出跨平台可重用結果。
func (s *service) DecideMessage(ctx context.Context, message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, botUserID string) *Decision {
	if message == nil {
		return &Decision{}
	}

	decision := &Decision{MentionedBot: message.MentionsUser(botUserID)}
	decision.EffectiveMentionedBot = decision.MentionedBot

	if s != nil && s.commandChain != nil && savedMessage != nil {
		onChain, err := s.commandChain.IsCommandChainMessage(ctx, savedMessage, decision.MentionedBot)
		if err != nil {
			decision.CommandChainError = err
		} else if onChain {
			decision.OnCommandChain = true
			decision.EffectiveMentionedBot = true
		}
	}

	if s != nil && s.semanticService != nil {
		classification, err := s.semanticService.ClassifyMessage(ctx, message, decision.EffectiveMentionedBot)
		if err != nil {
			decision.ClassificationError = err
		} else {
			decision.Classification = classification
		}
	}

	return decision
}
