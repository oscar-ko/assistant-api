package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ChannelTranslationMember records which users enabled translation for a channel.
type ChannelTranslationMember struct {
	ent.Schema
}

// Mixin of the ChannelTranslationMember.
func (ChannelTranslationMember) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the ChannelTranslationMember.
func (ChannelTranslationMember) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("channel_id", uuid.UUID{}),
		field.UUID("user_id", uuid.UUID{}),
		field.String("platform_user_id").Optional().Comment("平台上的使用者 ID (如 LINE userId)"),
	}
}

// Indexes of the ChannelTranslationMember.
func (ChannelTranslationMember) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel_id", "user_id").Unique(),
		index.Fields("channel_id", "platform_user_id"),
		index.Fields("platform_user_id"),
	}
}

// Edges of the ChannelTranslationMember.
func (ChannelTranslationMember) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("channel", Channel.Type).
			Field("channel_id").
			Required().
			Unique().
			Comment("translation 所屬頻道"),
		edge.To("user", User.Type).
			Field("user_id").
			Required().
			Unique().
			Comment("啟用 translation 的成員"),
	}
}
