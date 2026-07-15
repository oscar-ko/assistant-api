package actionpost

import (
	"context"
	"strings"

	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/ai/semanticdecision"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

const (
	// translationStartOperation 是翻譯啟用動作在 action 決策層的 operation 名稱。
	// Dispatcher 會用這個值把決策結果路由到對應 side-effect handler。
	translationStartOperation = "start_translation_locale"
)

// Handler 是 action 決策後的擴充處理函式簽名。
//
// 參數說明：
// - message: 統一訊息模型（跨平台 adapter 轉換後）
// - senderUserID: 平台來源使用者 ID（可為空，handler 內可再 fallback）
// - decision: final action decision（包含 api_operation 與 action_params）
// - matchedSkillCode: 上游候選比對出的 skill_code（可為空）
type Handler func(message *unifiedmessage.Message, senderUserID string, decision *semanticdecision.ActionDecision, matchedSkillCode string)

// Dispatcher 依 api_operation 分派 post-action handler。
//
// 設計目標：
// - 把「平台 webhook 流程」與「action 副作用執行」解耦
// - 新增 action 時，優先在這層註冊 handler，避免散落到各 provider
// - 讓 LINE/Slack/WhatsApp 等平台可共用同一份 post-action 邏輯
type Dispatcher struct {
	handlers map[string]Handler
}

// New 建立 dispatcher，並把 operation key 正規化為 trim+lower。
//
// 正規化策略可避免以下問題：
// - 上游決策含多餘空白
// - operation 大小寫不一致
//
// 若 key 為空或 handler 為 nil 會直接略過，避免 runtime dispatch 觸發 panic。
func New(handlers map[string]Handler) *Dispatcher {
	normalized := make(map[string]Handler, len(handlers))
	for operation, handler := range handlers {
		key := strings.ToLower(strings.TrimSpace(operation))
		if key == "" || handler == nil {
			continue
		}
		normalized[key] = handler
	}
	return &Dispatcher{handlers: normalized}
}

// NewDefaultDispatcher 提供目前通用的預設 handler 組合。
//
// 擴充方式：
// 1) 先實作新的 Handler（建議放在本 package）
// 2) 在此 map 註冊 operation -> handler
// 3) 各 provider 無需修改 dispatch 流程，即可共用新能力
func NewDefaultDispatcher(repo *repository.ChannelMessageRepo) *Dispatcher {
	return New(map[string]Handler{
		translationStartOperation: NewPersistTranslationCommandStateHandler(repo),
	})
}

// Dispatch 依 decision 的 api_operation 做 post-action 分派。
//
// 行為特性：
// - d 或 decision 為 nil 時靜默返回（不阻塞主流程）
// - 找不到 handler 時靜默返回（允許部分 action 尚未有 side-effect）
// - 不會回傳錯誤；錯誤由各 handler 內自行記錄與觀測
func (d *Dispatcher) Dispatch(message *unifiedmessage.Message, senderUserID string, decision *semanticdecision.ActionDecision, matchedSkillCode string) {
	if d == nil || decision == nil {
		return
	}
	operation := strings.ToLower(strings.TrimSpace(decision.APIOperation))
	handler, ok := d.handlers[operation]
	if !ok || handler == nil {
		return
	}
	handler(message, senderUserID, decision, matchedSkillCode)
}

// NewPersistTranslationCommandStateHandler 建立翻譯啟用副作用的共用 handler。
//
// 目前副作用包含：
// 1) 將發起者加入 channel_service_member
// 2) 將 target locale 寫入 translation_locales
//
// 錯誤策略：
// - 採「可觀測但不中斷主流程」：遇錯只記錄 warn 並返回
// - locale 寫入採逐筆容錯：單一 locale 失敗不影響其他 locale
func NewPersistTranslationCommandStateHandler(repo *repository.ChannelMessageRepo) Handler {
	return func(message *unifiedmessage.Message, senderUserID string, decision *semanticdecision.ActionDecision, matchedSkillCode string) {
		// 嚴格模式：必要依賴缺失時直接記錄 error，並停止本次副作用。
		if repo == nil || message == nil || decision == nil {
			zap.L().Error("translation command persistence failed: missing required dependency",
				zap.Bool("repo_nil", repo == nil),
				zap.Bool("message_nil", message == nil),
				zap.Bool("decision_nil", decision == nil),
			)
			return
		}
		// 只處理 start_translation_locale；其他 operation 由其他 handler 負責。
		if !strings.EqualFold(strings.TrimSpace(decision.APIOperation), translationStartOperation) {
			return
		}

		// 從通用 action_params 抽取 locale；缺參時視為契約錯誤。
		targetLocales := ExtractTranslationTargetLocales(decision)
		if len(targetLocales) == 0 {
			zap.L().Error("translation command persistence failed: missing required locale params",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("api_operation", strings.TrimSpace(decision.APIOperation)),
			)
			return
		}

		// ChannelID 在 unified model 內是字串，需先轉成 UUID 才能進入 repository 層。
		channelUUID, err := uuid.Parse(strings.TrimSpace(message.ChannelID))
		if err != nil || channelUUID == uuid.Nil {
			zap.L().Error("translation command persistence failed: invalid channel id",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Error(err),
			)
			return
		}

		// senderUserID 必須由 caller 提供；不再 fallback，避免來源不明。
		senderUserID = strings.TrimSpace(senderUserID)
		if senderUserID == "" || strings.EqualFold(senderUserID, "unknown") {
			zap.L().Error("translation command persistence failed: missing sender user id",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("sender_user_id", senderUserID),
			)
			return
		}

		// 先把平台 user id 解析成系統內 user id；解析失敗則不進行後續任何寫入。
		ownerUserID, err := repo.ResolveUserIDByLineUserID(context.Background(), senderUserID)
		if err != nil {
			zap.L().Error("translation command persistence failed: resolve owner user failed",
				zap.String("line_user_id", senderUserID),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Error(err),
			)
			return
		}
		// uuid.Nil 代表未綁定，視為錯誤。
		if ownerUserID == uuid.Nil {
			zap.L().Error("translation command persistence failed: line user not bound",
				zap.String("line_user_id", senderUserID),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			)
			return
		}

		// 嚴格模式：必須帶上游映射出的 skillCode，不再使用預設值。
		skillCode := strings.TrimSpace(matchedSkillCode)
		if skillCode == "" {
			zap.L().Error("translation command persistence failed: missing matched skill code",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("line_user_id", senderUserID),
			)
			return
		}
		// skillID 解析失敗或不存在都不應落庫，避免資料關聯錯誤。
		skillID, err := repo.ResolveSkillIDByCode(context.Background(), skillCode)
		if err != nil {
			zap.L().Error("translation command persistence failed: resolve skill failed",
				zap.String("skill_code", skillCode),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.Error(err),
			)
			return
		}
		if skillID == uuid.Nil {
			zap.L().Error("translation command persistence failed: skill not found",
				zap.String("skill_code", skillCode),
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			)
			return
		}

		// 先建立 service member，確保 locale 寫入前已具備服務成員關聯。
		if err := repo.AddServiceMemberToChannel(context.Background(), channelUUID, ownerUserID, skillID); err != nil {
			zap.L().Error("translation command persistence failed: add service member",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("line_user_id", senderUserID),
				zap.String("skill_code", skillCode),
				zap.Error(err),
			)
			return
		}

		// locale 逐筆寫入：單筆失敗繼續其餘語系，避免整批因局部錯誤失敗。
		appliedLocales := make([]string, 0, len(targetLocales))
		for _, targetLocale := range targetLocales {
			if err := repo.AddTranslationLocaleToChannel(context.Background(), channelUUID, skillID, ownerUserID, targetLocale); err != nil {
				zap.L().Error("translation command persistence failed: add target locale",
					zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
					zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
					zap.String("line_user_id", senderUserID),
					zap.String("skill_code", skillCode),
					zap.String("target_locale", targetLocale),
					zap.Error(err),
				)
				continue
			}
			appliedLocales = append(appliedLocales, targetLocale)
		}

		// 全失敗視為錯誤，直接輸出 error。
		if len(appliedLocales) == 0 {
			zap.L().Error("translation command persistence failed: all locale writes failed",
				zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
				zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
				zap.String("line_user_id", senderUserID),
				zap.String("skill_code", skillCode),
			)
			return
		}

		// 只有至少一個 locale 寫入成功時才記錄 success。
		zap.L().Info("line translation command state persisted",
			zap.String("channel_id", strings.TrimSpace(message.ChannelID)),
			zap.String("message_id", strings.TrimSpace(message.PlatformMessageID)),
			zap.String("line_user_id", senderUserID),
			zap.String("skill_code", skillCode),
			zap.Strings("target_locales", appliedLocales),
		)
	}
}

// ExtractTranslationTargetLocales 從通用 action_params 擷取翻譯語系參數。
//
// 支援兩種契約：
// - target_locale: 單值（字串）
// - target_locales: 多值（字串陣列）
//
// 輸出會經過 dedupeLocales 做清理與去重。
func ExtractTranslationTargetLocales(decision *semanticdecision.ActionDecision) []string {
	if decision == nil {
		return nil
	}
	locales := make([]string, 0)
	if value, ok := decision.ParamString(semanticdecision.ActionParamTargetLocale); ok {
		locales = append(locales, value)
	}
	locales = append(locales, decision.ParamStringSlice(semanticdecision.ActionParamTargetLocales)...)
	return dedupeLocales(locales)
}

// dedupeLocales 對 locale 清單做正規化去重。
//
// 規則：
// - 去除空白與空字串
// - 大小寫不敏感去重（en-US 與 en-us 視為同一值）
// - 保留第一個出現值的原始字面（維持可讀性與順序可預期）
func dedupeLocales(locales []string) []string {
	if len(locales) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(locales))
	out := make([]string, 0, len(locales))
	for _, locale := range locales {
		trimmed := strings.TrimSpace(locale)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
