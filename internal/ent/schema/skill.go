package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Skill stores high-level skill metadata.
type Skill struct {
	ent.Schema
}

func (Skill) Mixin() []ent.Mixin {
	return []ent.Mixin{IdMixin{}}
}

func (Skill) Fields() []ent.Field {
	return []ent.Field{
		field.String("skill_code").NotEmpty().Comment("技能代碼"),
		field.String("name").NotEmpty().Comment("技能名稱"),
		field.String("description").Optional().Nillable().Comment("技能描述").
			SchemaType(map[string]string{"mysql": "TEXT", "postgres": "text"}),
		// is_realtime 是 skill 自身的「能力宣告」，不是某個 channel 是否真的啟用服務的狀態。
		//
		// true 代表：這個 skill 在設計上可以參與非指令訊息的即時流程。
		// 例如使用者開啟翻譯後，後續一般聊天訊息不需要再 @bot 下 command，
		// 系統也可能即時產生翻譯 side-effect。
		//
		// false 代表：這個 skill 只透過明確 command/action flow 執行，
		// 一般非指令聊天訊息進來時不應主動處理。
		//
		// 注意：真正要不要對某個 channel 啟動即時流程，仍必須查 channel_service_members。
		// 只有「有人在該 channel 啟用這個 realtime skill」時，才算該 channel 有啟用服務。
		field.Bool("is_realtime").Default(false).Comment("是否會在非指令訊息進來時即時處理"),
		// requires_text_scan 是 realtime skill 的進一步能力宣告：
		// 它表示這個 skill 若在 channel 中被啟用，是否需要先對非指令文字訊息做分類掃描。
		//
		// true 代表：系統不能只靠固定設定判斷是否觸發，必須先讀取訊息語意，
		// 例如「明天下午提醒我交報告」這類一般聊天文字，可能需要 classifier 判斷是否屬於待辦提醒。
		//
		// false 代表：即使 skill 是 realtime，也不需要透過 classifier 判斷語意。
		// 例如 channel.translation 是否觸發，主要看 channel 中是否有人啟用翻譯、以及目標語系設定，
		// 不需要先把每則訊息送去分類模型。
		//
		// 判斷是否要 classification 的最終條件是：
		// channel_service_members 中存在同一 channel 的啟用紀錄，且該 skill 同時 is_realtime=true、requires_text_scan=true。
		// 因此這個欄位不是全域開關；它只描述 skill 類型，避免未啟用服務的 channel 也浪費 classifier 呼叫。
		field.Bool("requires_text_scan").Default(false).Comment("是否需要對非指令文字訊息做分類掃描"),
	}
}

func (Skill) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("skill_code").Unique(),
	}
}

func (Skill) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("actions", Action.Type),
		edge.From("channel_service_members", ChannelServiceMember.Type).Ref("skill"),
		edge.From("translation_locales", TranslationLocale.Type).Ref("skill"),
	}
}

func (Skill) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("技能分類"),
		entgql.QueryField(),
	}
}
