package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ActionResult stores command execution result for an action on a message.
type ActionResult struct {
	ent.Schema
}

// Mixin of the ActionResult.
func (ActionResult) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the ActionResult.
func (ActionResult) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("action_id", uuid.UUID{}).Comment("對應 action ID"),
		field.UUID("channel_message_id", uuid.UUID{}).Comment("觸發指令的訊息 ID"),
		field.Enum("status").Values("success", "missing_parameter", "failed").Comment("指令執行結果狀態"),
		field.String("result_message").Optional().Nillable().Comment("執行結果補充資訊（例如缺少參數名稱）"),
	}
}

// Indexes of the ActionResult.
func (ActionResult) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("action_id", "channel_message_id").Unique(),
		index.Fields("status"),
	}
}

// Edges of the ActionResult.
func (ActionResult) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("action", Action.Type).
			Field("action_id").
			Required().
			Unique().
			Comment("執行對應 action"),
		edge.To("channel_message", ChannelMessage.Type).
			Field("channel_message_id").
			Required().
			Unique().
			Comment("觸發該次執行的指令訊息"),
	}
}
