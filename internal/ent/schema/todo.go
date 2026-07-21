package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// Todo 是使用者擁有的正式待辦事項。
//
// Todo 是產品層乾淨的待辦資料表，一筆資料只屬於一個使用者。
// 使用者後續可以直接修改自己的 title、due_at 等欄位，因此正式 Todo 不能只是多人 assignee 清單的快照。
// 這裡只保存該使用者的「事、時、地、物」與正式待辦狀態。
// AI analyzer 的 decision、confidence、missing_fields、reason、linked message 等追蹤資料不放在這裡；需要排查來源時再透過 source_candidate 關聯回 TodoCandidate。
type Todo struct {
	ent.Schema
}

// Mixin of the Todo.
func (Todo) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the Todo.
func (Todo) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("channel_id", uuid.UUID{}).Immutable().Comment("正式待辦所屬 channel ID；產品查詢與權限邊界使用"),
		// owner_user_id 是 Todo 的產品主體：同一個對話 candidate 指派多人時，會拆成多筆 owner 不同的 Todo。
		// 後續使用者改標題、改時間或完成狀態時，只會影響自己的這一筆資料。
		field.UUID("owner_user_id", uuid.UUID{}).Immutable().Comment("此待辦所屬使用者 ID；每個人都有自己的 Todo row，可獨立修改標題與時間"),
		// source_candidate_id 只保留 AI promotion 來源，不參與 Todo 本身的人事時地物語意。
		// 手動建立的 Todo 沒有 AI evidence，因此允許為空。
		field.UUID("source_candidate_id", uuid.UUID{}).Optional().Nillable().Immutable().Comment("來源 Todo candidate ID；只用來回溯 AI 分析與 promotion evidence，人工建立的純 Todo 可為空"),
		field.Enum("status").Values("active", "completed", "cancelled").Default("active").Comment("正式待辦狀態；active 代表仍需追蹤或提醒"),
		field.String("title").Comment("事：正式待辦要完成的事項標題，供列表與提醒直接顯示"),
		field.Time("due_at").Optional().Nillable().Comment("時：正式提醒或到期時間；沒有時間的待辦可先為空"),
		field.String("due_timezone").Optional().Comment("時：due_at 使用的 IANA timezone，例如 Asia/Taipei"),
		field.Enum("due_precision").Values("datetime", "date", "relative_window", "unknown").Default("unknown").Comment("時：時間精度，描述 due_at 是精確時間、日期或相對區間"),
		field.String("location_text").Optional().Comment("地：待辦發生或交付的地點字面；目前 promotion 沒有抽到時保持空值"),
		field.String("object_text").Optional().Comment("物：待辦涉及的文件、物品、系統或交付物字面；目前 promotion 沒有抽到時保持空值"),
	}
}

// Edges of the Todo.
func (Todo) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("channel", Channel.Type).Field("channel_id").Required().Unique().Immutable().Comment("正式待辦所屬 channel"),
		edge.To("owner", User.Type).Field("owner_user_id").Required().Unique().Immutable().Comment("此待辦所屬使用者；同一個 candidate 若指派多人，會 promotion 成多筆不同 owner 的 Todo"),
		edge.To("source_candidate", TodoCandidate.Type).Field("source_candidate_id").Unique().Immutable().Comment("AI promotion 來源；只保存分析/evidence 關聯，不作為 Todo 五要素的主要讀取來源"),
		// events 是正式 Todo 的變更時間線；讀取目前狀態仍以 Todo 欄位為準，
		// 只有需要解釋「為什麼時間被改掉」或追查 AI 套用來源時才查這條 edge。
		edge.To("events", TodoEvent.Type).Comment("此 Todo 的狀態變更事件；保存 AI promotion 與後續更新歷史"),
	}
}

// Indexes of the Todo.
func (Todo) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel_id", "status"),
		// 使用者常見查詢是「我的未完成待辦」，因此 owner/status 需要同一個 channel 邊界內的複合索引。
		index.Fields("channel_id", "owner_user_id", "status"),
		index.Fields("channel_id", "due_at"),
		// 同一個 candidate 可以 promotion 成多個 owner 的 Todo，但同一 owner 只能有一筆，避免重試造成重複待辦。
		index.Fields("source_candidate_id", "owner_user_id").Unique(),
	}
}

// Annotations of the Todo.
func (Todo) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Todo Reminder 正式待辦事項"),
		entgql.QueryField(),
	}
}
