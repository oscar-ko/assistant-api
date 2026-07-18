package commanddecision

import (
	"context"
	"strings"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/usecase/inbound/commandchain"

	"github.com/google/uuid"
)

// Decision 表示單則訊息的共用判斷結果。
type Decision struct {
	IsMentionedBot          bool
	IsOnCommandChain        bool
	IsEffectiveMentionedBot bool
	IsPrivateChannel        bool
	CommandChainError       error
}

// IsCommand 回傳這則訊息是否屬於 command 類型。
func (d *Decision) IsCommand() bool {
	if d == nil {
		return false
	}
	return d.IsPrivateChannel || d.IsMentionedBot || d.IsOnCommandChain
}

// Service 封裝跨平台共用的指令判斷流程。
type Service interface {
	DecideMessage(ctx context.Context, message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, botUserID string) *Decision
}

type service struct {
	commandChain commandchain.Service
}

// NewService 建立共用指令判斷服務。
func NewService(commandChain commandchain.Service) Service {
	if commandChain == nil {
		return nil
	}
	return &service{commandChain: commandChain}
}

// DecideMessage 依序執行 mention 與訊息鍊判斷，輸出跨平台可重用結果。
func (s *service) DecideMessage(ctx context.Context, message *unifiedmessage.Message, savedMessage *ent.ChannelMessage, botUserID string) *Decision {
	if message == nil {
		// 沒有訊息可判斷時，回傳空 Decision 讓上層安全處理。
		return &Decision{}
	}

	// 第一階段：先判斷訊息是否直接 mention bot。
	decision := &Decision{IsMentionedBot: message.MentionsUser(botUserID)}
	if strings.EqualFold(strings.TrimSpace(message.ChannelType), "private") {
		decision.IsPrivateChannel = true
	}
	commandMode := decision.IsPrivateChannel || decision.IsMentionedBot
	// reply context 規則：
	// 即使本訊息沒有 mention / 非 private，只要它是回覆鏈的一部分，
	// 仍需進一步進 command chain 判斷，避免把補參數訊息誤判成一般對話。
	// triggered_message_id 表示系統觸發鏈，reply_to_msg_id 表示平台回覆鏈；
	// 任一存在都代表此訊息可能需要沿父節點重用既有指令。
	hasReplyContext := savedMessage != nil && ((savedMessage.TriggeredMessageID != nil && *savedMessage.TriggeredMessageID != uuid.Nil) || strings.TrimSpace(savedMessage.ReplyToMsgID) != "")
	// 只有「非 commandMode 且非 reply context」才提早返回。
	if !commandMode && !hasReplyContext {
		return decision
	}
	// isEffectiveMentionedBot 會作為後續 command chain 判斷的規則輸入。
	decision.IsEffectiveMentionedBot = decision.IsMentionedBot
	if decision.IsPrivateChannel {
		decision.IsEffectiveMentionedBot = true
	}

	if s != nil && s.commandChain != nil && savedMessage != nil {
		// 第二階段：若在指令訊息鍊上，強制視為有效 mention。
		onChain, err := s.commandChain.IsCommandChainMessage(ctx, savedMessage, decision.IsMentionedBot)
		if err != nil {
			// 鍊判斷失敗只記錄錯誤，不中斷整體判斷流程。
			decision.CommandChainError = err
		} else if onChain {
			decision.IsOnCommandChain = true
			decision.IsEffectiveMentionedBot = true
		}
	}

	return decision
}
