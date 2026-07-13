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
	"assistant-api/internal/usecase/ai/embedding"

	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

type defaultSkillSeed struct {
	SkillCode   string
	Name        string
	Description string
	Actions     []defaultActionSeed
}

type defaultActionSeed struct {
	ActionCode     action.ActionCode
	Name           string
	Description    string
	APIOperation   string
	RouteTexts     []string
	CommandPurpose string
}

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
					CommandPurpose: "目的: 取得啟用待辦提醒的指令與作用範圍。手段: 若缺少必要資訊，透過追問請使用者明確提供。",
				},
				{
					ActionCode:     action.ActionCodeDisable,
					Name:           "Stop Todo Reminder",
					Description:    "Disable todo reminder in the current channel.",
					APIOperation:   "stop_todo_reminder",
					RouteTexts:     []string{"關閉待辦提醒", "停止待辦提醒", "不要再提醒待辦"},
					CommandPurpose: "目的: 取得停用待辦提醒的指令與作用範圍。手段: 若語意不完整，透過追問確認。",
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
					CommandPurpose: "目的: 得到開啟翻譯的指令及翻譯的語言種類。手段: 只接受使用者明確指定的目標語系；禁止推測預設語系或聊天語言。若未提供語系，回 missing_target 並追問請使用者提供目標語系。",
				},
				{
					ActionCode:     action.ActionCodeDisable,
					Name:           "Stop Translation All",
					Description:    "Disable translation service globally in the current channel.",
					APIOperation:   "stop_translation_all",
					RouteTexts:     []string{"關閉翻譯服務", "停止所有翻譯", "不要翻譯了", "把翻譯功能全部關掉"},
					CommandPurpose: "目的: 取得關閉整體翻譯服務的明確指令。",
				},
				{
					ActionCode:     action.ActionCodeConfigure,
					Name:           "Stop Translation Locale",
					Description:    "Disable translation for a specific locale in the current channel.",
					APIOperation:   "stop_translation_locale",
					RouteTexts:     []string{"關閉某語言翻譯", "停止指定語言翻譯", "移除翻譯語系", "把某個語言的翻譯關掉", "不要某語言翻譯"},
					CommandPurpose: "目的: 取得停用指定翻譯語系的指令與目標語系。手段: 只接受使用者明確指定的目標語系；禁止推測預設語系或聊天語言。若缺語系，回 missing_target 並透過提問請使用者提供目標語系。",
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
					CommandPurpose: "目的: 取得啟用 Gmail 監控的明確操作意圖。手段: 若授權或範圍不明確，透過追問釐清。",
				},
				{
					ActionCode:     action.ActionCodeDisable,
					Name:           "Unbind Gmail Watch",
					Description:    "Disable Gmail watch and unbind notifications.",
					APIOperation:   "gmail_watch_unbind",
					RouteTexts:     []string{"關閉 Gmail 監控", "解除 Gmail watch"},
					CommandPurpose: "目的: 取得解除 Gmail 監控/綁定的明確指令。手段: 必要時追問使用者確認解除範圍。",
				},
				{
					ActionCode:     action.ActionCodeQueryStatus,
					Name:           "Gmail Watch Status",
					Description:    "Query current Gmail watch status.",
					APIOperation:   "gmail_watch_status",
					RouteTexts:     []string{"Gmail 監控狀態", "查詢 Gmail watch 狀態"},
					CommandPurpose: "目的: 取得查詢 Gmail 監控狀態的需求。手段: 若查詢目標不明，透過追問請使用者補充目標資訊。",
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
					CommandPurpose: "目的: 取得使用者查詢私人頻道清單的意圖。手段: 若範圍不明，透過追問確認。",
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

func upsertSeedAction(ctx context.Context, client *ent.Client, skillID uuid.UUID, seed defaultActionSeed) (*ent.Action, error) {
	operation := strings.TrimSpace(seed.APIOperation)
	if operation == "" {
		return nil, fmt.Errorf("api_operation is required")
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
			SetAPIOperation(operation)
		if desc != "" {
			create.SetDescription(desc)
		}
		return create.Save(ctx)
	}

	update := client.Action.UpdateOneID(node.ID).SetName(strings.TrimSpace(seed.Name)).SetAPIOperation(operation)
	if purpose := strings.TrimSpace(seed.CommandPurpose); purpose != "" {
		update.SetCommandPurpose(purpose)
	}
	if desc != "" {
		update.SetDescription(desc)
	}
	return update.Save(ctx)
}

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

func buildEmbeddingClientForSeed() embedding.Service {
	if strings.TrimSpace(config.AI.EmbeddingURL) == "" {
		return nil
	}
	return embedding.NewClient(config.AI.EmbeddingURL, config.AI.EmbeddingTimeoutSeconds, config.AI.EmbeddingPath)
}

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
