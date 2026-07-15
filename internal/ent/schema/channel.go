package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Channel stores inbound conversation context by platform and group identity.
type Channel struct {
	ent.Schema
}

// Mixin of the Channel.
func (Channel) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the Channel.
func (Channel) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().Comment("頻道名稱（群組名或對話名稱）"),
		field.Enum("platform").Values("line", "whatsapp", "slack", "telegram").Default("line").Comment("來源平台"),
		field.String("group_id").NotEmpty().Comment("平台上的頻道識別碼（groupId/roomId/userId）"),
		field.Enum("type").Values("group", "private").Default("group").Comment("頻道型別：群組或私訊"),
		field.Bool("is_active").Default(true).Comment("是否啟用該頻道處理"),
		field.Int("inactive_message_count").Default(0).Comment("停用期間累計訊息數"),
	}
}

// Edges of the Channel.
func (Channel) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("messages", ChannelMessage.Type).Comment("此頻道底下的訊息列表"),
		edge.From("service_members", ChannelServiceMember.Type).Ref("channel").Comment("此頻道啟用服務的成員"),
	}
}

// Indexes of the Channel.
func (Channel) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("platform", "group_id").Unique(),
	}
}

// Annotations of the Channel.
func (Channel) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("聊天平台頻道"),
		entgql.QueryField(),
	}
}
