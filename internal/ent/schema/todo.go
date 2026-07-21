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
// Candidate 保存模型抽取、時間正規化與訊息上下文；Todo 則只代表產品層已可追蹤、提醒或顯示的正式狀態。
// 因此這張表不重複 summary、assignees、due_at 等 candidate 已有欄位，顯示與排程資料一律透過 source_candidate edge 讀取。
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
		field.UUID("source_candidate_id", uuid.UUID{}).Immutable().Comment("來源 Todo candidate ID；用來回溯 analyzer/promotion 判斷"),
		field.Enum("status").Values("active", "completed", "cancelled").Default("active").Comment("正式待辦狀態；active 代表仍需追蹤或提醒"),
		field.String("promotion_reason").Optional().Comment("promotion gate 通過或取消正式待辦的原因，供 debug/audit 使用"),
	}
}

// Edges of the Todo.
func (Todo) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("source_candidate", TodoCandidate.Type).Field("source_candidate_id").Required().Unique().Immutable().Comment("正式待辦唯一的資料來源；channel、訊息、摘要、時間與 assignee 都從 candidate 讀取"),
	}
}

// Indexes of the Todo.
func (Todo) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status"),
		index.Fields("source_candidate_id").Unique(),
	}
}

// Annotations of the Todo.
func (Todo) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Todo Reminder 正式待辦事項"),
		entgql.QueryField(),
	}
}
