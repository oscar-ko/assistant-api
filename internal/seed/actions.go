package seed

import (
	"context"
	"fmt"
	"log"
	"strings"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/ent/action"
	"assistant-api/internal/ent/actionroute"
	"assistant-api/internal/ent/skill"
	aiembedding "assistant-api/internal/integration/ai/embedding"
	aillminteraction "assistant-api/internal/integration/ai/llm_interaction"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

// defaultSkillSeed 描述一個 skill 的初始化資料。
// 這個結構僅用於 seeding 階段，對應 skill 與其底下 action 的宣告式配置。
type defaultSkillSeed struct {
	SkillCode   string
	Name        string
	Description string
	Actions     []defaultActionSeed
}

// defaultActionSeed 描述單一 action 的初始化資料。
// RouteTexts 會寫入 action_route，用於語意路由與向量檢索；
// CommandPurpose 會保存到 action.command_purpose，提供後續語意決策參考。
type defaultActionSeed struct {
	ActionCode     action.ActionCode
	Name           string
	Description    string
	APIOperation   string
	RouteTexts     []string
	CommandPurpose string
}

// seedActionCatalog 初始化技能/動作/路由三層資料。
// 流程：
// 1) 先 upsert skill
// 2) 再 upsert 該 skill 下每個 action
// 3) 再 upsert action route 語句
// 4) 最後補齊缺失的 route embedding
// 任何一步失敗都會中止，避免寫入部分不一致的種子資料。
func seedActionCatalog(ctx context.Context, client *ent.Client) error {
	seeds := []defaultSkillSeed{
		{
			SkillCode:   "todo.reminder",
			Name:        "Todo Reminder Skill",
			Description: "Enable or disable todo reminder behaviors for channels.",
			Actions: []defaultActionSeed{
				{
					ActionCode:     action.ActionCodeEnable,
					Name:           "Start Todo Reminder",
					Description:    "Enable todo reminder in the current channel.",
					APIOperation:   "start_todo_reminder",
					RouteTexts:     []string{"開啟待辦提醒", "開始待辦提醒", "幫我開始提醒待辦"},
					CommandPurpose: "用途: 協助模型判斷此指令是啟用待辦提醒。必要參數: channel_scope(目前頻道或指定頻道)。缺參策略: 若 channel_scope 不明確，先提問確認作用範圍後再執行。",
				},
				{
					ActionCode:     action.ActionCodeDisable,
					Name:           "Stop Todo Reminder",
					Description:    "Disable todo reminder in the current channel.",
					APIOperation:   "stop_todo_reminder",
					RouteTexts:     []string{"關閉待辦提醒", "停止待辦提醒", "不要再提醒待辦"},
					CommandPurpose: "用途: 協助模型判斷此指令是停用待辦提醒。必要參數: channel_scope(目前頻道或指定頻道)。缺參策略: 若未明確指出作用範圍，先提問確認再執行。",
				},
			},
		},
		{
			SkillCode:   "channel.translation",
			Name:        "Channel Translation Skill",
			Description: "Enable/disable translation features for channels.",
			Actions: []defaultActionSeed{
				{
					ActionCode:     action.ActionCodeEnable,
					Name:           "Start Translation Locale",
					Description:    "Enable translation for a specific locale in the current channel.",
					APIOperation:   "start_translation_locale",
					RouteTexts:     []string{"開啟翻譯", "開始翻譯模式", "新增翻譯語系", "幫我開啟某語言翻譯", "請翻譯成指定語言"},
					CommandPurpose: fmt.Sprintf("用途: 啟用翻譯語系。規則: 只接受使用者明確指定的目標語系；禁止推測預設語系、聊天語言或從上下文自動補值。若使用者以自然語言提及語言名稱（如 中文/英文/日文 或 Chinese/English/Japanese），視為已明確指定語系，必須將其正規化為 action_params.target_locales 的語系碼。輸出格式: action_params.target_locales 必須是字串陣列，元素格式僅允許語言碼(xx)或語系碼(xx-YY)，例如 en、ja、zh-TW；不可輸出自然語言名稱。若輸入包含 [command_chain_context] 且 missing_parameters 含 %s，代表補參數階段；此時若使用者提及一個或多個語言名稱，必須直接 next_step=execute_action 並輸出 action_params.target_locales。若沒有 [command_chain_context]，代表初次決策階段；僅在使用者完全未提及任何可映射語系時，才可 next_step=ask_clarifying_question 且 missing_parameters=[%s]。", aillminteraction.ActionParamTargetLocales, aillminteraction.ActionParamTargetLocales),
				},
				{
					ActionCode:     action.ActionCodeDisable,
					Name:           "Stop Translation All",
					Description:    "Disable translation service globally in the current channel.",
					APIOperation:   "stop_translation_all",
					RouteTexts:     []string{"關閉翻譯服務", "停止所有翻譯", "不要翻譯了", "把翻譯功能全部關掉"},
					CommandPurpose: "用途: 關閉整體翻譯服務。規則: 直接判定為 stop_translation_all，不需語系參數。",
				},
				{
					ActionCode:     action.ActionCodeConfigure,
					Name:           "Stop Translation Locale",
					Description:    "Disable translation for a specific locale in the current channel.",
					APIOperation:   "stop_translation_locale",
					RouteTexts:     []string{"關閉某語言翻譯", "停止指定語言翻譯", "移除翻譯語系", "把某個語言的翻譯關掉", "不要某語言翻譯"},
					CommandPurpose: fmt.Sprintf("用途: 停用指定翻譯語系。規則: 只接受使用者明確指定的目標語系；禁止推測預設語系、聊天語言或從上下文自動補值。若使用者以自然語言提及語言名稱（如 中文/英文/日文 或 Chinese/English/Japanese），視為已明確指定語系，必須將其正規化為 action_params.target_locales 的語系碼。輸出格式: action_params.target_locales 必須是字串陣列，元素格式僅允許語言碼(xx)或語系碼(xx-YY)，例如 en、ja、zh-TW；不可輸出自然語言名稱。若輸入包含 [command_chain_context] 且 missing_parameters 含 %s，代表補參數階段；此時若使用者提及一個或多個語言名稱，必須直接 next_step=execute_action 並輸出 action_params.target_locales。若沒有 [command_chain_context]，代表初次決策階段；僅在使用者完全未提及任何可映射語系時，才可 next_step=ask_clarifying_question 且 missing_parameters=[%s]。", aillminteraction.ActionParamTargetLocales, aillminteraction.ActionParamTargetLocales),
				},
			},
		},
		{
			SkillCode:   "gmail.watch",
			Name:        "Gmail Watch Skill",
			Description: "Manage gmail watch and oauth binding status.",
			Actions: []defaultActionSeed{
				{
					ActionCode:     action.ActionCodeEnable,
					Name:           "Start Gmail Watch",
					Description:    "Enable Gmail push watch.",
					APIOperation:   "gmail_watch_start",
					RouteTexts:     []string{"開啟 Gmail 監控", "開始 Gmail watch"},
					CommandPurpose: "用途: 協助模型判斷此指令是啟用 Gmail 監控。必要參數: account_binding(授權帳號是否已綁定)、watch_scope。缺參策略: 若綁定狀態或範圍不明確，先提問確認再執行。",
				},
				{
					ActionCode:     action.ActionCodeDisable,
					Name:           "Unbind Gmail Watch",
					Description:    "Disable Gmail watch and unbind notifications.",
					APIOperation:   "gmail_watch_unbind",
					RouteTexts:     []string{"關閉 Gmail 監控", "解除 Gmail watch"},
					CommandPurpose: "用途: 協助模型判斷此指令是解除 Gmail 監控綁定。必要參數: account_binding、unbind_scope。缺參策略: 若解除對象或範圍不明，先提問確認後再執行。",
				},
				{
					ActionCode:     action.ActionCodeQueryStatus,
					Name:           "Gmail Watch Status",
					Description:    "Query current Gmail watch status.",
					APIOperation:   "gmail_watch_status",
					RouteTexts:     []string{"Gmail 監控狀態", "查詢 Gmail watch 狀態"},
					CommandPurpose: "用途: 協助模型判斷此指令是查詢 Gmail 監控狀態。必要參數: target_account(可選但建議明確)。缺參策略: 若使用者有多帳號且未指明，先提問確認查詢對象。",
				},
			},
		},
		{
			SkillCode:   "channel.discovery",
			Name:        "Channel Discovery Skill",
			Description: "Discover channel and private channel metadata.",
			Actions: []defaultActionSeed{
				{
					ActionCode:     action.ActionCodeQueryStatus,
					Name:           "List Private Channels",
					Description:    "List private channels available to the user.",
					APIOperation:   "list_private_channels",
					RouteTexts:     []string{"列出私人頻道", "查看我有哪些私訊頻道"},
					CommandPurpose: "用途: 協助模型判斷此指令是查詢私人頻道清單。必要參數: user_scope(通常為目前使用者)。缺參策略: 若查詢對象不明確，先提問確認查詢目標。",
				},
			},
		},
	}

	for _, seed := range seeds {
		skillNode, err := upsertSeedSkill(ctx, client, seed)
		if err != nil {
			return err
		}
		for _, actionSeed := range seed.Actions {
			actionNode, err := upsertSeedAction(ctx, client, skillNode.ID, actionSeed)
			if err != nil {
				return err
			}
			if err := upsertSeedActionRoutes(ctx, client, actionNode.ID, "zh-TW", actionSeed.RouteTexts); err != nil {
				return err
			}
		}
	}

	if err := backfillActionRouteEmbeddings(ctx, client); err != nil {
		return err
	}

	return nil
}

// upsertSeedSkill 以 skill_code 作為唯一鍵，建立或更新 skill。
// - create: 當 skill 不存在時建立。
// - update: 當 skill 已存在時更新名稱與描述。
// 描述欄位採「有值才覆寫」策略，避免把既有內容覆蓋為空字串。
func upsertSeedSkill(ctx context.Context, client *ent.Client, seed defaultSkillSeed) (*ent.Skill, error) {
	skillCode := strings.TrimSpace(seed.SkillCode)
	if skillCode == "" {
		return nil, fmt.Errorf("skill_code is required")
	}

	node, err := client.Skill.Query().Where(skill.SkillCodeEQ(skillCode)).Only(ctx)
	desc := strings.TrimSpace(seed.Description)
	if err != nil {
		if !ent.IsNotFound(err) {
			return nil, err
		}
		create := client.Skill.Create().SetSkillCode(skillCode).SetName(strings.TrimSpace(seed.Name))
		if desc != "" {
			create.SetDescription(desc)
		}
		return create.Save(ctx)
	}

	update := client.Skill.UpdateOneID(node.ID).SetName(strings.TrimSpace(seed.Name))
	if desc != "" {
		update.SetDescription(desc)
	}
	return update.Save(ctx)
}

// upsertSeedAction 以 (skill_id, action_code) 作為 action 的唯一定位鍵。
// - APIOperation 為執行層路由識別，必填。
// - CommandPurpose 為 prompt 注入資訊，必填，create/update 都會寫入。
// - 描述欄位同樣採有值才覆寫。
func upsertSeedAction(ctx context.Context, client *ent.Client, skillID uuid.UUID, seed defaultActionSeed) (*ent.Action, error) {
	operation := strings.TrimSpace(seed.APIOperation)
	if operation == "" {
		return nil, fmt.Errorf("api_operation is required")
	}
	purpose := strings.TrimSpace(seed.CommandPurpose)
	if purpose == "" {
		return nil, fmt.Errorf("command_purpose is required for api_operation=%s", operation)
	}

	node, err := client.Action.Query().Where(action.SkillIDEQ(skillID), action.ActionCodeEQ(seed.ActionCode)).Only(ctx)
	desc := strings.TrimSpace(seed.Description)
	if err != nil {
		if !ent.IsNotFound(err) {
			return nil, err
		}
		create := client.Action.Create().
			SetSkillID(skillID).
			SetActionCode(seed.ActionCode).
			SetName(strings.TrimSpace(seed.Name)).
			SetAPIOperation(operation).
			SetCommandPurpose(purpose)
		if desc != "" {
			create.SetDescription(desc)
		}
		return create.Save(ctx)
	}

	update := client.Action.UpdateOneID(node.ID).SetName(strings.TrimSpace(seed.Name)).SetAPIOperation(operation)
	update.SetCommandPurpose(purpose)
	if desc != "" {
		update.SetDescription(desc)
	}
	return update.Save(ctx)
}

// upsertSeedActionRoutes 將 action 的語句變體寫入 action_route。
// - locale 空值時預設使用 zh-TW，確保資料一致。
// - route text 會 trim 並忽略空字串。
// - 若已存在同 action/locale/text 的記錄則跳過，保持可重複執行（idempotent）。
func upsertSeedActionRoutes(ctx context.Context, client *ent.Client, actionID uuid.UUID, locale string, routeTexts []string) error {
	trimmedLocale := strings.TrimSpace(locale)
	if trimmedLocale == "" {
		trimmedLocale = "zh-TW"
	}

	for _, routeText := range routeTexts {
		text := strings.TrimSpace(routeText)
		if text == "" {
			continue
		}
		_, err := client.ActionRoute.Query().Where(actionroute.ActionIDEQ(actionID), actionroute.LocaleEQ(trimmedLocale), actionroute.RouteTextEQ(text)).Only(ctx)
		if err != nil {
			if !ent.IsNotFound(err) {
				return err
			}
			create := client.ActionRoute.Create().SetActionID(actionID).SetLocale(trimmedLocale).SetRouteText(text)
			if _, err := create.Save(ctx); err != nil {
				return err
			}
			continue
		}
	}
	return nil
}

// backfillActionRouteEmbeddings 為尚未有 embedding 的 action_route 回填向量。
// 設計重點：
// - 若未配置 embedding 服務，直接跳過（不阻塞服務啟動）。
// - 單筆失敗不中止整批，累計 failed 並繼續，以提高可用性。
// - 只有「全部失敗且沒有任何成功」才回傳錯誤，避免暫時性失敗造成整體不可用。
func backfillActionRouteEmbeddings(ctx context.Context, client *ent.Client) error {
	embedder := buildEmbeddingClientForSeed()
	if embedder == nil {
		return nil
	}

	routes, err := client.ActionRoute.Query().Where(actionroute.EmbeddingIsNil()).All(ctx)
	if err != nil {
		return err
	}
	if len(routes) == 0 {
		return nil
	}

	updated := 0
	failed := 0
	for _, route := range routes {
		if route == nil {
			continue
		}
		text := strings.TrimSpace(route.RouteText)
		if text == "" {
			continue
		}
		vec, embErr := embedder.GetEmbedding(ctx, text)
		if embErr != nil || len(vec) == 0 {
			log.Printf("failed to embed route %s: %v", route.ID.String(), embErr)
			failed++
			continue
		}
		stored := pgvector.NewVector(toFloat32Slice(vec))
		if _, updateErr := client.ActionRoute.UpdateOneID(route.ID).SetEmbedding(stored).Save(ctx); updateErr != nil {
			log.Printf("failed to save embedding for route %s: %v", route.ID.String(), updateErr)
			failed++
			continue
		}
		updated++
	}

	log.Printf("action route embedding backfill completed: updated=%d, failed=%d", updated, failed)
	if failed > 0 && updated == 0 {
		return fmt.Errorf("all %d routes failed to embed", failed)
	}
	return nil
}

// buildEmbeddingClientForSeed 依設定建立 embedding client。
// 若未提供 embedding_url，回傳 nil 表示目前環境不啟用向量回填。
func buildEmbeddingClientForSeed() aiembedding.Service {
	_, profile, err := config.ResolveLocalProviderProfile(config.LLMProviders, config.AI.Embedding.Target)
	if err != nil {
		return nil
	}
	return aiembedding.NewClient(
		profile.URL,
		config.AI.Embedding.TimeoutSeconds,
		profile.Path,
		config.AI.Embedding.MaxAttempts,
		config.AI.Embedding.RetryBackoffMS,
		config.AI.Embedding.AliveProbeIntervalMS,
		config.AI.Embedding.AliveProbeTimeoutMS,
		config.AI.Embedding.AliveSuccessTTLMS,
		config.AI.Embedding.AliveFailureCooldownMS,
	)
}

// toFloat32Slice 將 embedding API 回傳的 float64 轉為 pgvector 所需的 float32。
func toFloat32Slice(vec []float64) []float32 {
	if len(vec) == 0 {
		return nil
	}
	out := make([]float32, len(vec))
	for i, v := range vec {
		out[i] = float32(v)
	}
	return out
}
