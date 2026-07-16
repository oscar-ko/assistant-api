package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ActionSuccessMessage stores successful command message to action mapping.
type ActionSuccessMessage struct {
	ent.Schema
}

// Mixin of the ActionSuccessMessage.
func (ActionSuccessMessage) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the ActionSuccessMessage.
func (ActionSuccessMessage) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("action_id", uuid.UUID{}).Comment("對應 action ID"),
		field.UUID("channel_message_id", uuid.UUID{}).Comment("成功執行的指令訊息 ID"),
	}
}

// Indexes of the ActionSuccessMessage.
func (ActionSuccessMessage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("action_id", "channel_message_id").Unique(),
		index.Fields("channel_message_id").Unique(),
	}
}

// Edges of the ActionSuccessMessage.
func (ActionSuccessMessage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("action", Action.Type).
			Field("action_id").
			Required().
			Unique().
			Comment("成功訊息對應 action"),
		edge.To("channel_message", ChannelMessage.Type).
			Field("channel_message_id").
			Required().
			Unique().
			Comment("成功執行的指令訊息"),
	}
}
