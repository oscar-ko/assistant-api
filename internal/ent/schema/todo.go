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

// Todo 是由 TodoCandidate promotion 後形成的正式待辦事項。
//
// Candidate 保存模型抽取與上下文判斷的原始結果；Todo 則代表產品層已可追蹤、提醒或顯示的正式事項。
// 因此這張表只接收已通過 promotion gate 的資料，保留 source_candidate_id 回溯完整分析紀錄。
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
		field.UUID("channel_id", uuid.UUID{}).Immutable().Comment("正式待辦所屬 channel ID"),
		field.UUID("source_candidate_id", uuid.UUID{}).Immutable().Comment("來源 Todo candidate ID；用來回溯 analyzer/promotion 判斷"),
		field.UUID("source_message_id", uuid.UUID{}).Immutable().Comment("建立來源 candidate 的第一則訊息 ID"),
		field.UUID("last_message_id", uuid.UUID{}).Comment("最近一次更新此待辦內容的訊息 ID"),
		field.Enum("status").Values("active", "completed", "cancelled").Default("active").Comment("正式待辦狀態；active 代表仍需追蹤或提醒"),
		field.String("title").Comment("正式待辦標題；由 candidate summary 轉入，必須是可顯示給使用者的內容"),
		field.JSON("assignees", []string{}).Optional().Comment("正式待辦負責人字面名稱快照；解析細節仍回 source candidate assignee evidence"),
		field.Time("due_at").Comment("正式待辦提醒時間；promotion 只在 due_text 已正規化後建立 Todo"),
		field.String("due_timezone").Comment("due_at 使用的 IANA timezone，例如 Asia/Taipei"),
		field.Enum("due_precision").Values("datetime", "date", "relative_window", "unknown").Default("unknown").Comment("時間正規化精度，沿用 candidate due_precision"),
		field.Float("confidence").Default(0).Comment("來源 candidate 最近一次 analyzer decision 信心分數"),
		field.String("promotion_reason").Optional().Comment("promotion gate 通過或取消正式待辦的原因，供 debug/audit 使用"),
	}
}

// Edges of the Todo.
func (Todo) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("channel", Channel.Type).Field("channel_id").Required().Unique().Immutable().Comment("正式待辦所屬 channel"),
		edge.To("source_candidate", TodoCandidate.Type).Field("source_candidate_id").Required().Unique().Immutable().Comment("建立正式待辦的來源 candidate"),
		edge.To("source_message", ChannelMessage.Type).Field("source_message_id").Required().Unique().Immutable().Comment("來源 candidate 的第一則訊息"),
		edge.To("last_message", ChannelMessage.Type).Field("last_message_id").Required().Unique().Comment("最近一次更新正式待辦的訊息"),
	}
}

// Indexes of the Todo.
func (Todo) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel_id", "status"),
		index.Fields("channel_id", "due_at"),
		index.Fields("channel_id", "source_candidate_id").Unique(),
		index.Fields("channel_id", "source_message_id"),
	}
}

// Annotations of the Todo.
func (Todo) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Todo Reminder 正式待辦事項"),
		entgql.QueryField(),
	}
}
