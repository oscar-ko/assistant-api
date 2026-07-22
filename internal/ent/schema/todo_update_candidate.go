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

// TodoUpdateCandidate 保存「已存在正式 Todo」的待確認變更。
//
// TodoCandidate 負責把對話抽成候選待辦；TodoUpdateCandidate 則負責正式 Todo 已建立後，
// 後續訊息提出的新時間、標題或取消等變更。這層先保存 proposed_values/current_values，
// 不直接覆蓋 Todo，讓產品可以依信心、來源 linkage 或使用者確認再決定是否 apply。
type TodoUpdateCandidate struct {
	ent.Schema
}

// Mixin of the TodoUpdateCandidate.
func (TodoUpdateCandidate) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the TodoUpdateCandidate.
func (TodoUpdateCandidate) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("todo_id", uuid.UUID{}).Immutable().Comment("此更新候選要套用到的正式 Todo ID"),
		field.UUID("source_candidate_id", uuid.UUID{}).Immutable().Comment("產生此更新候選的 Todo candidate ID"),
		field.UUID("source_message_id", uuid.UUID{}).Optional().Nillable().Immutable().Comment("觸發此更新候選的訊息 ID"),
		field.Enum("change_type").Values("updated", "cancelled").Comment("候選變更類型；目前先支援更新欄位與取消"),
		field.Enum("status").Values("pending", "requires_confirmation", "applied", "rejected").Default("requires_confirmation").Comment("更新候選狀態；requires_confirmation 代表不可自動覆蓋正式 Todo"),
		field.JSON("current_values", map[string]any{}).Optional().Comment("正式 Todo 目前產品欄位快照"),
		field.JSON("proposed_values", map[string]any{}).Optional().Comment("模型從後續訊息提出的產品欄位快照"),
		field.Float("confidence").Default(0).Comment("來源 candidate 最近一次模型信心值"),
		field.String("reason").Optional().Comment("模型提出此更新候選的理由"),
	}
}

// Edges of the TodoUpdateCandidate.
func (TodoUpdateCandidate) Edges() []ent.Edge {
	return []ent.Edge{
		// 每筆 update candidate 只指向一個 Todo；Todo.update_candidates 是一對多反查。
		edge.From("todo", Todo.Type).Ref("update_candidates").Field("todo_id").Required().Unique().Immutable().Comment("此更新候選的目標 Todo"),
		// Ent 的 Unique 表示本候選只指向單一來源，不表示同一來源只能產生一筆更新候選。
		edge.To("source_candidate", TodoCandidate.Type).Field("source_candidate_id").Required().Unique().Immutable().Comment("更新候選來源 candidate"),
		edge.To("source_message", ChannelMessage.Type).Field("source_message_id").Unique().Immutable().Comment("更新候選來源訊息"),
	}
}

// Indexes of the TodoUpdateCandidate.
func (TodoUpdateCandidate) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("todo_id", "status"),
		index.Fields("source_candidate_id"),
		index.Fields("source_message_id"),
		index.Fields("change_type", "status"),
	}
}

// Annotations of the TodoUpdateCandidate.
func (TodoUpdateCandidate) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Todo 待確認更新候選"),
		entgql.QueryField(),
	}
}
