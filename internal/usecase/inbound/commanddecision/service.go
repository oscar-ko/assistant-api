package commanddecision

import (
	"context"
	"strings"

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

// IsCommand 回傳最終是否可視為指令訊息。
// 目前以語意分類結果 intent_label=command 作為單一布林判斷。
func (d *Decision) IsCommand() bool {
	if d == nil || d.Classification == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(d.Classification.IntentLabel), "command")
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
		// 沒有訊息可判斷時，回傳空 Decision 讓上層安全處理。
		return &Decision{}
	}

	// 第一階段：先判斷訊息是否直接 mention bot。
	decision := &Decision{MentionedBot: message.MentionsUser(botUserID)}
	// effectiveMentionedBot 會作為後續 semantic classify 的規則輸入。
	decision.EffectiveMentionedBot = decision.MentionedBot

	if s != nil && s.commandChain != nil && savedMessage != nil {
		// 第二階段：若在指令訊息鍊上，強制視為有效 mention。
		onChain, err := s.commandChain.IsCommandChainMessage(ctx, savedMessage, decision.MentionedBot)
		if err != nil {
			// 鍊判斷失敗只記錄錯誤，不中斷整體判斷流程。
			decision.CommandChainError = err
		} else if onChain {
			decision.OnCommandChain = true
			decision.EffectiveMentionedBot = true
		}
	} // TODO: fix this

	if s != nil && s.semanticService != nil {
		// 第三階段：交給 semantic service 輸出 intent_label 與信心分數。
		classification, err := s.semanticService.ClassifyMessage(ctx, message, decision.EffectiveMentionedBot)
		if err != nil {
			// 分類失敗同樣不拋錯，由呼叫端決定 fallback 行為。
			decision.ClassificationError = err
		} else {
			decision.Classification = classification
		}
	} // Todo:Ha ha

	return decision
}
