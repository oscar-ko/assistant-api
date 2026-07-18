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

// ChannelMessage 儲存跨平台正規化後的訊息資料。
//
// 關聯語意刻意拆成兩條：
// - reply_to_msg_id 保留平台原生「回覆哪一則訊息」的識別。
// - triggered_message_id 只描述系統 outbound 訊息由哪一則 inbound 訊息觸發。
// 這樣 command chain 回溯時能分清「使用者在平台上回覆」與「系統因指令產生輸出」。
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
		field.UUID("triggered_message_id", uuid.UUID{}).Optional().Nillable().Comment("系統訊息觸發來源訊息 ID；只記錄內部系統輸出由哪則訊息衍生，平台回覆目標仍使用 reply_to_msg_id"),
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
		edge.To("triggered_message", ChannelMessage.Type).Unique().Field("triggered_message_id").Comment("此系統訊息由哪一則訊息觸發；用於 command chain 與系統通知回溯"),
		edge.From("triggered_messages", ChannelMessage.Type).Ref("triggered_message").Comment("由此訊息觸發產生的系統訊息集合；同一來源可觸發多筆輸出"),
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
