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

// ChannelMessage stores normalized inbound messages from messaging platforms.
type ChannelMessage struct {
	ent.Schema
}

// Mixin of the ChannelMessage.
func (ChannelMessage) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the ChannelMessage.
func (ChannelMessage) Fields() []ent.Field {
	return []ent.Field{
		field.String("content").NotEmpty().Comment("訊息內容"),
		field.UUID("channel_id", uuid.UUID{}).Immutable().Comment("所屬頻道 ID"),
		field.UUID("related_message_id", uuid.UUID{}).Optional().Nillable().Comment("關聯上一則訊息 ID"),
		field.String("platform_tenant_id").Optional().Comment("平台租戶/工作區識別（例如 Slack team ID、Teams tenant ID）"),
		field.String("sender_id").Comment("訊息發送者平台 ID"),
		field.UUID("sender_user_id", uuid.UUID{}).Optional().Nillable().Comment("映射後的系統內 user ID"),
		field.String("sender_name").Optional().Comment("訊息發送者顯示名稱"),
		field.String("platform_message_id").Optional().Comment("平台原始訊息 ID"),
		field.String("reply_to_msg_id").Optional().Comment("平台回覆目標訊息 ID"),
		field.String("message_type").Default("text").Comment("訊息型別，例如 text/image/audio"),
		field.Int64("platform_timestamp").Optional().Comment("平台事件時間戳（毫秒）"),
	}
}

// Edges of the ChannelMessage.
func (ChannelMessage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("channel", Channel.Type).Ref("messages").Field("channel_id").Required().Immutable().Unique().Comment("訊息所屬頻道"),
		edge.To("related_message", ChannelMessage.Type).Unique().Field("related_message_id").Comment("關聯到被回覆/引用的上一則訊息"),
		edge.From("replies", ChannelMessage.Type).Ref("related_message").Comment("以本訊息為基準的回覆訊息"),
	}
}

// Indexes of the ChannelMessage.
func (ChannelMessage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel_id", "platform_message_id"),
	}
}

// Annotations of the ChannelMessage.
func (ChannelMessage) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("聊天平台訊息"),
		entgql.QueryField(),
	}
}
