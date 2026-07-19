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

// ChannelMessageMention 保存單一訊息中的 structured mention 事實。
//
// mention 屬於訊息層資料，不屬於 Todo domain；Todo candidate 之後會透過 message_id
// 回頭讀取這些 mention，再決定哪些 mention 應該變成 candidate assignee。
// bot mention 也一樣保存，只用 identity_kind/is_bot 標註，避免 ingestion 階段提早丟資料。
type ChannelMessageMention struct {
	ent.Schema
}

// Mixin of the ChannelMessageMention.
func (ChannelMessageMention) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the ChannelMessageMention.
func (ChannelMessageMention) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("channel_message_id", uuid.UUID{}).Immutable().Comment("所屬 channel message ID"),
		field.Enum("platform").Values("line", "whatsapp", "slack", "telegram").Default("line").Comment("mention 來源平台"),
		field.String("platform_user_id").Optional().Comment("平台原始使用者 ID；LINE mentionee.userId 或 Slack user id"),
		field.UUID("user_id", uuid.UUID{}).Optional().Nillable().Comment("解析後的系統 user ID；未綁定或 bot 可為空"),
		field.String("display_text").Optional().Comment("原訊息中的 mention 顯示文字，例如 @Amy"),
		field.Int("mention_index").Optional().Nillable().Comment("mention 在原文字中的起始位置；平台未提供時為空"),
		field.Int("mention_length").Optional().Nillable().Comment("mention 在原文字中的長度；平台未提供時為空"),
		field.String("mention_type").Default("user").Comment("平台 payload 的 mention 類型，例如 user/all/here"),
		field.Enum("identity_kind").Values("user", "bot", "unknown").Default("user").Comment("系統解析後的身分種類；bot 也保留為一般 mention 事實"),
		field.Bool("is_bot").Default(false).Comment("是否為機器人帳號 mention；方便查詢與後續 assignee resolver 判斷"),
		field.Enum("resolution_status").Values("resolved", "unresolved", "ambiguous", "unsupported").Default("unresolved").Comment("platform identity 是否已解析成系統 user"),
		field.String("raw").Optional().Comment("原平台 mentionee JSON 片段，供除錯與未來欄位擴充使用"),
	}
}

// Edges of the ChannelMessageMention.
func (ChannelMessageMention) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("message", ChannelMessage.Type).Ref("mentions").Field("channel_message_id").Required().Immutable().Unique().Comment("mention 所屬訊息"),
		edge.From("todo_candidate_assignees", TodoCandidateAssignee.Type).Ref("source_message_mention").Comment("使用此 message mention 產生的 Todo assignee evidence"),
		edge.To("user", User.Type).Unique().Field("user_id").Comment("mention 解析出的系統使用者；未解析或 bot 可為空"),
	}
}

// Indexes of the ChannelMessageMention.
func (ChannelMessageMention) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel_message_id"),
		index.Fields("platform", "platform_user_id"),
		index.Fields("user_id"),
	}
}

// Annotations of the ChannelMessageMention.
func (ChannelMessageMention) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("聊天平台訊息 mention"),
		entgql.QueryField(),
	}
}
