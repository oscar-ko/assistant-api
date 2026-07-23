package realtime

import (
	"context"
	"strings"
	"time"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/runtimecontext"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

// MessageSender 定義多平台推送訊息能力。
//
// 為何不直接依賴 LINE/Slack SDK：
// - 讓 AutoTranslateService 成為平台無關邏輯，避免綁死單一 provider。
// - 各平台只需包一層 adapter，就能共用翻譯流程。
type MessageSender interface {
	SendText(ctx context.Context, chatID string, userID string, text string, replyRef string, quoteRef string) (string, error)
}

// PlatformUserResolver 解析平台 user id 到系統內 user id。
//
// 權限檢查必須基於系統內 user id，
// 因此平台身分（lineUserId/slackUserId）要先透過 resolver 正規化。
type PlatformUserResolver func(ctx context.Context, platformUserID string) (uuid.UUID, error)

// AutoTranslateServiceOptions 提供自動翻譯服務所需依賴。
//
// 這些依賴都是可替換的：
// - Repo: 讀取服務啟用狀態與落庫
// - Sender: 發送翻譯結果
// - Translator: 呼叫翻譯模型
// - ResolveOwnerUserID: 平台使用者映射
// - TranslationSkill/BotSenderID/PlatformLabel: 行為與 observability 參數
type AutoTranslateServiceOptions struct {
	Repo               *repository.ChannelMessageRepo
	Sender             MessageSender
	Translator         Translator
	LanguageDetector   LanguageDetector
	ResolveOwnerUserID PlatformUserResolver
	TranslationSkill   string
	BotSenderID        string
	PlatformLabel      string
	Now                func() time.Time
}

// AutoTranslateService 在非指令訊息路徑執行即時翻譯。
//
// 核心目標：
// - 把「翻譯是否應觸發」與「翻譯結果如何推送/落庫」封裝成可重用模組。
// - webhook 僅負責分派，不再承載大量翻譯細節。
type AutoTranslateService struct {
	repo               *repository.ChannelMessageRepo
	sender             MessageSender
	translator         Translator
	languageDetector   LanguageDetector
	resolveOwnerUserID PlatformUserResolver
	translationSkill   string
	botSenderID        string
	platformLabel      string
	now                func() time.Time
}

// NewAutoTranslateService 建立即時翻譯服務。
//
// 預設值策略：
// - TranslationSkill 未提供時使用 channel.translation（seed 穩定代碼）。
// - Now 未提供時回落到 time.Now，方便測試注入固定時間。
func NewAutoTranslateService(options AutoTranslateServiceOptions) *AutoTranslateService {
	skill := strings.TrimSpace(options.TranslationSkill)
	if skill == "" {
		skill = "channel.translation"
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &AutoTranslateService{
		repo:               options.Repo,
		sender:             options.Sender,
		translator:         options.Translator,
		languageDetector:   options.LanguageDetector,
		resolveOwnerUserID: options.ResolveOwnerUserID,
		translationSkill:   skill,
		botSenderID:        strings.TrimSpace(options.BotSenderID),
		platformLabel:      strings.TrimSpace(options.PlatformLabel),
		now:                now,
	}
}

// Handle 嘗試對非指令文字訊息執行即時翻譯。
//
// 完整 gating 流程（全部通過才會送出翻譯）：
// 1) 依賴完整（repo/sender/translator/resolver）
// 2) 訊息為文字且內容非空
// 3) 可解析平台使用者
// 4) 可解析翻譯 skill 與 owner user
// 5) owner user 在該 channel 確實啟用翻譯服務
// 6) 該 channel skill 具備 target locales
// 7) 若可靠偵測出來源語言，排除同語言 target locales
// 8) 翻譯成功且輸出非空
//
// 任何一關失敗都直接 return（fail-fast），避免不明確狀態造成誤推播。
func (s *AutoTranslateService) Handle(ctx context.Context, messageCtx MessageContext) {
	if s == nil || s.repo == nil || s.sender == nil || s.translator == nil || s.languageDetector == nil || s.resolveOwnerUserID == nil {
		return
	}
	if ctx == nil {
		// 統一補背景 context，讓下游 repo/sender/translator 不需重複防禦。
		ctx = context.Background()
	}
	message := messageCtx.Message
	if message == nil || !message.IsText() {
		return
	}
	originalText := strings.TrimSpace(message.Text)
	if originalText == "" {
		return
	}
	platformUserID := strings.TrimSpace(messageCtx.PlatformUserID)
	if platformUserID == "" {
		return
	}

	// 以 skill code 解析 skill id，避免在程式硬編碼 UUID。
	skillID, err := s.repo.ResolveSkillIDByCode(ctx, s.translationSkill)
	if err != nil || skillID == uuid.Nil {
		if err != nil {
			zap.L().Warn("realtime translation skipped: resolve translation skill failed",
				zap.String("platform", s.platformLabel),
				zap.Error(err),
			)
		} else {
			zap.L().Info("realtime translation skipped: translation skill not found",
				zap.String("platform", s.platformLabel),
				zap.String("skill_code", s.translationSkill),
			)
		}
		return
	}

	// 先把平台 user id 映射為系統 user，才有辦法做權限/啟用判斷。
	ownerUserID, err := s.resolveOwnerUserID(ctx, platformUserID)
	if err != nil || ownerUserID == uuid.Nil {
		if err != nil {
			zap.L().Warn("realtime translation skipped: resolve sender user failed",
				zap.String("platform", s.platformLabel),
				zap.String("platform_user_id", platformUserID),
				zap.Error(err),
			)
		} else {
			zap.L().Info("realtime translation skipped: sender user is not bound",
				zap.String("platform", s.platformLabel),
				zap.String("platform_user_id", platformUserID),
			)
		}
		return
	}

	// channel 優先使用已落庫資料；若沒有再以平台欄位補解析。
	channelID, ok := s.resolveChannelID(ctx, message, messageCtx.SavedMessage)
	if !ok {
		return
	}

	// 嚴格檢查「此人於此頻道」是否啟用翻譯，避免對未啟用對象誤觸發。
	enabled, err := s.repo.HasChannelServiceMember(ctx, channelID, ownerUserID, skillID)
	if err != nil {
		zap.L().Warn("realtime translation skipped: query service member failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", channelID.String()),
			zap.String("platform_user_id", platformUserID),
			zap.Error(err),
		)
		return
	}
	if !enabled {
		zap.L().Info("realtime translation skipped: sender has not enabled translation service",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", channelID.String()),
			zap.String("platform_user_id", platformUserID),
			zap.String("owner_user_id", ownerUserID.String()),
			zap.String("skill_id", skillID.String()),
		)
		return
	}

	// 目標語系由資料庫設定決定，不做語系猜測。
	targetLocales, err := s.repo.ListChannelSkillTargetLocales(ctx, channelID, skillID)
	if err != nil {
		zap.L().Warn("realtime translation skipped: list target locales failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", channelID.String()),
			zap.String("platform_user_id", platformUserID),
			zap.Error(err),
		)
		return
	}
	if len(targetLocales) == 0 {
		zap.L().Info("realtime translation skipped: no target locales configured",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", channelID.String()),
			zap.String("platform_user_id", platformUserID),
			zap.String("owner_user_id", ownerUserID.String()),
			zap.String("skill_id", skillID.String()),
		)
		return
	}

	if sourceLanguage, err := s.languageDetector.DetectLanguage(ctx, originalText); err != nil {
		zap.L().Warn("realtime translation source language not filtered: detect source language failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", channelID.String()),
			zap.String("platform_user_id", platformUserID),
			zap.Strings("target_locales", targetLocales),
			zap.Error(err),
		)
	} else if strings.TrimSpace(sourceLanguage) != "" {
		originalTargetLocales := append([]string(nil), targetLocales...)
		targetLocales = excludeSourceLanguageLocales(targetLocales, sourceLanguage)
		if len(targetLocales) == 0 {
			zap.L().Info("realtime translation skipped: all target locales match source language",
				zap.String("platform", s.platformLabel),
				zap.String("channel_id", channelID.String()),
				zap.String("platform_user_id", platformUserID),
				zap.String("source_language", strings.TrimSpace(sourceLanguage)),
				zap.Strings("target_locales", originalTargetLocales),
			)
			return
		}
	}

	// 翻譯邏輯委派給 Translator，保持 service 對 transport 無感。
	translations, err := s.translator.Translate(ctx, originalText, targetLocales)
	if err != nil {
		zap.L().Warn("realtime translation failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Strings("target_locales", targetLocales),
			zap.Error(err),
		)
		return
	}

	// 按 targetLocales 順序組裝輸出，維持使用者可預期的呈現順序。
	outboundText := composeTranslations(targetLocales, translations)
	if outboundText == "" {
		return
	}

	// 即時翻譯採主動推送，不依賴 reply token；引用資訊沿用 QuoteRef。
	sentPlatformMessageID, sendErr := s.sender.SendText(
		ctx,
		strings.TrimSpace(message.ChannelID),
		"",
		outboundText,
		"",
		strings.TrimSpace(messageCtx.QuoteRef),
	)
	if sendErr != nil {
		zap.L().Warn("realtime translation push failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.Strings("target_locales", targetLocales),
			zap.Error(sendErr),
		)
		return
	}

	// 推送成功後嘗試落庫；落庫失敗只記錄告警，不回滾已送出的訊息。
	s.persistSentMessage(ctx, messageCtx.SavedMessage, message, outboundText, targetLocales, sentPlatformMessageID)
}

func (s *AutoTranslateService) resolveChannelID(ctx context.Context, message *unifiedmessage.Message, savedMessage *ent.ChannelMessage) (uuid.UUID, bool) {
	// 有已落庫訊息時優先使用其 channel id，可減少重複查詢與資料分歧。
	if savedMessage != nil && savedMessage.ChannelID != uuid.Nil {
		return savedMessage.ChannelID, true
	}
	// 保底路徑：只做查詢，不在即時流程自動建立 channel。
	// 理由：channel 的生命週期由綁定流程負責，
	// 即時翻譯屬於訊息副作用，不可越權初始化 channel。
	channelNode, err := s.repo.GetChannelByPlatformGroupID(
		ctx,
		strings.TrimSpace(message.Platform),
		strings.TrimSpace(message.ChannelID),
	)
	if err != nil || channelNode == nil {
		if err != nil {
			zap.L().Warn("realtime translation skipped: resolve channel failed",
				zap.String("platform", s.platformLabel),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.Error(err),
			)
		} else {
			zap.L().Info("realtime translation skipped: channel not found",
				zap.String("platform", s.platformLabel),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			)
		}
		// 無 channel 時直接略過，避免產生「翻譯訊息存在但主訊息未綁定」的不一致資料。
		return uuid.Nil, false
	}
	return channelNode.ID, true
}

func composeTranslations(targetLocales []string, translations map[string]string) string {
	// 依 targetLocales 順序組裝，確保輸出順序穩定且可預期。
	sections := make([]string, 0, len(targetLocales))
	for _, targetLocale := range targetLocales {
		locale := strings.TrimSpace(targetLocale)
		if locale == "" {
			continue
		}
		translated := strings.TrimSpace(translations[locale])
		if translated == "" {
			continue
		}
		// 不加語系標籤或符號前綴，讓推播內容只保留翻譯文字本身。
		sections = append(sections, translated)
	}
	if len(sections) == 0 {
		return ""
	}
	return strings.Join(sections, "\n\n")
}

func excludeSourceLanguageLocales(targetLocales []string, sourceLanguage string) []string {
	source := normalizeLanguageCode(sourceLanguage)
	if source == "" || len(targetLocales) == 0 {
		return nil
	}
	filtered := make([]string, 0, len(targetLocales))
	seen := make(map[string]struct{}, len(targetLocales))
	for _, targetLocale := range targetLocales {
		locale := strings.TrimSpace(targetLocale)
		if locale == "" {
			continue
		}
		if _, exists := seen[locale]; exists {
			continue
		}
		seen[locale] = struct{}{}
		if normalizeLanguageCode(locale) == source {
			continue
		}
		filtered = append(filtered, locale)
	}
	return filtered
}

func normalizeLanguageCode(locale string) string {
	trimmed := strings.ToLower(strings.TrimSpace(locale))
	if trimmed == "" {
		return ""
	}
	trimmed = strings.ReplaceAll(trimmed, "_", "-")
	if index := strings.Index(trimmed, "-"); index >= 0 {
		trimmed = trimmed[:index]
	}
	return trimmed
}

func (s *AutoTranslateService) persistSentMessage(ctx context.Context, savedMessage *ent.ChannelMessage, inbound *unifiedmessage.Message, text string, targetLocales []string, sentPlatformMessageID string) {
	// 若沒有 inbound 對應落庫訊息，直接略過，不嘗試建立孤兒紀錄。
	if s.repo == nil || savedMessage == nil {
		return
	}
	botSenderID := strings.TrimSpace(runtimecontext.BotSenderIDFromContext(ctx))
	if botSenderID == "" {
		botSenderID = s.botSenderID
	}
	_, err := s.repo.SaveSentMessage(
		ctx,
		savedMessage.ChannelID,
		botSenderID,
		"",
		sentPlatformMessageID,
		// 自動翻譯訊息目前以一般 outbound 保存，不宣告平台 reply 目標；
		// 內部來源仍由最後一個參數 savedMessage.ID 建立 triggered_message_id。
		"",
		text,
		"text",
		s.now().UnixMilli(),
		savedMessage.ID,
	)
	if err != nil {
		zap.L().Warn("persist realtime translation message failed",
			zap.String("platform", s.platformLabel),
			zap.String("channel_id", strings.TrimSpace(inbound.ChannelID)),
			zap.String("message_id", strings.TrimSpace(inbound.PlatformMessageID)),
			zap.Strings("target_locales", targetLocales),
			zap.Error(err),
		)
	}
}
