package messagepipeline

import (
	"context"
	"strings"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/provider/realtime"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/usecase/inbound/commanddecision"
	"assistant-api/internal/usecase/inbound/conversationflow"
	"assistant-api/internal/usecase/inbound/messagepersist"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Handler 負責執行「Webhook 事件已轉成 unifiedmessage.Message 之後」的共用主流程。
//
// 設計邊界：
//   - 不解析 LINE / Slack / WhatsApp 的原始 webhook payload。
//   - 不處理平台生命週期事件，例如 bot join / leave、Slack challenge。
//   - 只接收已正規化的 unified message，以及少量仍需由平台提供的上下文。
//
// 這個抽象的目的，是讓各 provider webhook service 只保留平台差異：
// adapter、signature 驗證、reply/thread token、workspace bot id 等；
// 而持久化、command 判斷、非指令 realtime side-effect、command flow 則集中在這裡維護。
type Handler struct {
	// PlatformLabel 只用於 log，例如 line / slack。
	// 不應被下游用來做業務分支，避免重新引入 provider-specific hardcode。
	PlatformLabel string
	// Persistence 先把訊息落到 channel_message，並負責嚴格檢查 channel 是否已綁定。
	// 若找不到 channel，PersistUnifiedMessage 會回 nil，本流程會直接停止。
	Persistence messagepersist.Service
	// Decision 封裝 mention、private channel、reply command chain 等共用 command gate。
	// nil 時仍會使用本檔的最小判斷，讓流程在部分依賴未注入時可安全略過。
	Decision commanddecision.Service
	// NonCommandDispatcher 執行非指令訊息的即時 side-effect，例如翻譯或分類。
	// 這些服務只在訊息已成功落庫且不屬於 command 時才會被呼叫。
	NonCommandDispatcher *realtime.Dispatcher
	// CommandFlow 執行 command 的 top-k、LLM decision、action dispatch 與 outbound notice。
	// provider 只需要提供符合 conversationflow.OutboundMessenger 的發送實作。
	CommandFlow *conversationflow.Orchestrator
}

// Input 是共用流程需要的最小 provider context。
//
// Message 是核心資料，必須已經由 provider adapter 正規化完成。
// 其他欄位保留為 provider context，是因為它們在不同平台有不同來源：
// LINE 可能來自 replyToken / quoteToken，Slack 可能來自 thread ts / workspace bot id。
type Input struct {
	// Context 可帶入 provider-specific runtime value，例如 Slack workspace team id 或 bot sender id。
	// nil 時會補 context.Background()，避免呼叫端重複防禦。
	Context context.Context
	// Message 是跨平台統一訊息。若為 nil，流程會直接返回 nil。
	Message *unifiedmessage.Message
	// BotUserID 用於判斷 message 是否 mention bot。
	// Slack 需依 workspace 解析；LINE 則通常來自固定設定。
	BotUserID string
	// PlatformUserID 是平台使用者 id，供 command flow 與 realtime service 反查內部使用者。
	PlatformUserID string
	// ReplyRef 是平台回覆參考，例如 LINE reply token 或 Slack thread ts。
	ReplyRef string
	// QuoteRef 是平台引用參考；平台不支援時可留空。
	QuoteRef string
}

// Process 執行共用訊息主流程，回傳已落庫的 ChannelMessage。
//
// 流程順序固定如下：
//  1. 基本防禦：handler 或 message 為 nil 時直接返回。
//  2. 持久化訊息：只有已綁定 channel 的訊息可以繼續往下走。
//  3. command 判斷：判斷 private、mention bot、reply command chain。
//  4. 非指令訊息：交給 realtime dispatcher 處理翻譯、分類等 side-effect。
//  5. 指令訊息：交給 conversationflow.Orchestrator 做 AI 決策與 action 執行。
//
// 這裡刻意採 fail-fast：沒有 channel、沒有 message、沒有可用流程時直接停止，
// 不做預設 channel 建立、不補 fallback，也不把 provider 差異藏進共用層。
func (h *Handler) Process(input Input) *ent.ChannelMessage {
	// 沒有 handler 或 message 時沒有任何可處理的資料，直接返回即可。
	if h == nil || input.Message == nil {
		return nil
	}
	// 允許 provider 傳入帶有 runtime value 的 context；未提供時補背景 context。
	ctx := input.Context
	if ctx == nil {
		ctx = context.Background()
	}

	// 第一個真正的業務閘門：訊息必須能對應到既有 channel 才能繼續。
	// Persistence 內部只查詢、不自動建立 channel；這可避免未綁定來源污染資料。
	savedMessage := h.Persistence.PersistUnifiedMessage(ctx, input.Message)
	if savedMessage == nil {
		zap.L().Debug(strings.TrimSpace(h.PlatformLabel)+" message skipped: channel unavailable",
			zap.String("channel_id", strings.TrimSpace(input.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(input.Message.PlatformMessageID)),
		)
		return nil
	}

	// 先建立最小 decision，確保 Decision service 未注入時仍有明確且保守的判斷。
	// private channel 視為 command mode；群組訊息則至少要 mention bot 才進 command。
	decision := &commanddecision.Decision{IsMember: isMemberMessage(savedMessage), IsMentionedBot: input.Message.MentionsUser(input.BotUserID)}
	if strings.EqualFold(strings.TrimSpace(input.Message.ChannelType), "private") {
		decision.IsPrivateChannel = true
	}
	decision.IsEffectiveMentionedBot = decision.IsMentionedBot
	// 若有完整 Decision service，交給它補上 reply command chain 判斷。
	// private flag 在 service 後再同步一次，避免不同 provider adapter 的 private 判斷遺漏。
	if h.Decision != nil {
		decision = h.Decision.DecideMessage(ctx, input.Message, savedMessage, input.BotUserID)
		if decision != nil && strings.EqualFold(strings.TrimSpace(input.Message.ChannelType), "private") {
			decision.IsPrivateChannel = true
		}
	}

	// 非 command 訊息不進 AI command flow；但可觸發翻譯、分類等 realtime side-effect。
	// 這些 side-effect 的細節由 dispatcher 裡的各服務自行判斷，pipeline 不做業務分支。
	if decision == nil || !decision.IsCommand() {
		if h.NonCommandDispatcher != nil {
			h.NonCommandDispatcher.Handle(ctx, realtime.MessageContext{
				Message:        input.Message,
				SavedMessage:   savedMessage,
				PlatformUserID: strings.TrimSpace(input.PlatformUserID),
				QuoteRef:       strings.TrimSpace(input.QuoteRef),
			})
		}
		return savedMessage
	}

	// command chain 判斷失敗不阻斷 command 本身；只記錄觀測資訊。
	// 真正是否可執行 action，仍由後續 conversationflow 與 dispatcher 嚴格把關。
	if decision.CommandChainError != nil {
		zap.L().Debug("command chain check skipped",
			zap.String("channel_id", strings.TrimSpace(input.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(input.Message.PlatformMessageID)),
			zap.Error(decision.CommandChainError),
		)
	} else if decision.IsOnCommandChain {
		zap.L().Info("command chain message",
			zap.String("channel_id", strings.TrimSpace(input.Message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(input.Message.PlatformMessageID)),
			zap.Bool("mentioned_bot", decision.IsMentionedBot),
			zap.Bool("effective_mentioned_bot", decision.IsEffectiveMentionedBot),
			zap.String("reply_to_msg_id", strings.TrimSpace(input.Message.ReplyToMsgID)),
		)
	}

	// 指令訊息交給既有 conversationflow。pipeline 只負責傳遞上下文，
	// 不直接做 top-k、LLM prompt、action dispatch 或 outbound persistence。
	if h.CommandFlow != nil {
		h.CommandFlow.ProcessCommand(
			ctx,
			input.Message,
			savedMessage,
			strings.TrimSpace(input.PlatformUserID),
			strings.TrimSpace(input.ReplyRef),
			strings.TrimSpace(input.QuoteRef),
		)
	}
	return savedMessage
}

func isMemberMessage(message *ent.ChannelMessage) bool {
	return message != nil && message.SenderUserID != nil && *message.SenderUserID != uuid.Nil
}
