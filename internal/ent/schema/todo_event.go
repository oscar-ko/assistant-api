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

// TodoEvent 保存正式 Todo 的狀態變更歷史。
//
// Todo 保存產品目前狀態；TodoEvent 保存它如何由 candidate/message 推進而來。
// 這讓後續「已建立 Todo 又被訊息改時間」可以追溯 old/new 值，而不是無痕覆蓋。
// 事件表不重新承擔 Todo 的查詢主體責任：列表與提醒仍讀 Todo，目前表只做 audit trail，
// 讓 AI 自動套用更新時可以回查每一次變更來自哪則訊息與哪個 candidate。
type TodoEvent struct {
	ent.Schema
}

// Mixin of the TodoEvent.
func (TodoEvent) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the TodoEvent.
func (TodoEvent) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("todo_id", uuid.UUID{}).Immutable().Comment("事件所屬正式 Todo ID"),
		field.UUID("source_candidate_id", uuid.UUID{}).Optional().Nillable().Immutable().Comment("觸發此事件的 Todo candidate ID；人工事件可為空"),
		field.UUID("source_message_id", uuid.UUID{}).Optional().Nillable().Immutable().Comment("觸發此事件的訊息 ID；人工事件可為空"),
		field.Enum("event_type").Values("created", "updated", "cancelled").Comment("Todo 狀態事件類型"),
		// old_values/new_values 使用 JSON snapshot，而不是為 title/due_at/status 等欄位各建歷史欄位，
		// 讓後續若 Todo 增加 location/object 等產品欄位，也能在不調整事件表結構的情況下保存變更前後值。
		field.JSON("old_values", map[string]any{}).Optional().Comment("事件前的產品欄位快照；created 時為空物件"),
		field.JSON("new_values", map[string]any{}).Optional().Comment("事件後的產品欄位快照"),
		field.Float("confidence").Default(0).Comment("來源 candidate 最近一次模型信心值"),
		field.String("reason").Optional().Comment("來源 candidate 或系統套用此事件的理由"),
	}
}

// Edges of the TodoEvent.
func (TodoEvent) Edges() []ent.Edge {
	return []ent.Edge{
		// TodoEvent 透過 todo_id 屬於單一 Todo；Todo.events 則是一對多反查。
		// 這裡的 Unique 是 Ent 對「本 event 只能指向一筆 Todo」的語意，不代表 Todo 只能有一筆 event。
		edge.From("todo", Todo.Type).Ref("events").Field("todo_id").Required().Unique().Immutable().Comment("事件所屬正式 Todo"),
		// source_candidate/source_message 是事件來源，不是產品主體。
		// Unique 同樣表示每筆 event 只會指向一個來源；多筆 event 仍可共享同一個 candidate 或 message。
		edge.To("source_candidate", TodoCandidate.Type).Field("source_candidate_id").Unique().Immutable().Comment("事件來源 candidate"),
		edge.To("source_message", ChannelMessage.Type).Field("source_message_id").Unique().Immutable().Comment("事件來源訊息"),
	}
}

// Indexes of the TodoEvent.
func (TodoEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("todo_id", "created_at"),
		index.Fields("source_candidate_id"),
		index.Fields("source_message_id"),
		index.Fields("event_type"),
	}
}

// Annotations of the TodoEvent.
func (TodoEvent) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Todo 狀態變更事件"),
		entgql.QueryField(),
	}
}