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

// TodoCandidateAssignee 保存 Todo candidate 的 assignee 解析快照。
//
// 這張表不取代 channel_message_mentions：mention 表保存訊息事實，這張表保存 Todo domain
// 對那些訊息事實的使用結果。之後 candidate promotion 建正式個人 todo 時，會以這張表作 owner 來源。
type TodoCandidateAssignee struct {
	ent.Schema
}

// Mixin of the TodoCandidateAssignee.
func (TodoCandidateAssignee) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the TodoCandidateAssignee.
func (TodoCandidateAssignee) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("candidate_id", uuid.UUID{}).Immutable().Comment("所屬 Todo candidate ID"),
		field.UUID("source_message_mention_id", uuid.UUID{}).Optional().Nillable().Comment("來源 message mention ID；非 mention 來源可為空"),
		field.UUID("user_id", uuid.UUID{}).Optional().Nillable().Comment("解析後的系統 user ID；未綁定、bot 或 ambiguous 可為空"),
		field.Enum("source").Values("mention", "analyzer", "sender", "reply_context").Default("mention").Comment("assignee 來源：structured mention、模型字面、sender 或 reply context"),
		field.Enum("platform").Values("line", "whatsapp", "slack", "telegram").Default("line").Comment("assignee 來源平台"),
		field.String("platform_user_id").Optional().Comment("平台原始使用者 ID，例如 LINE userId 或 Slack user id"),
		field.String("display_text").Optional().Comment("assignee 顯示文字，例如 @Amy 或模型抽取的人名"),
		field.Enum("identity_kind").Values("user", "bot", "unknown").Default("user").Comment("解析後身分種類；bot 也保存但不等於個人 todo owner"),
		field.Bool("is_bot").Default(false).Comment("是否為機器人 assignee 快照"),
		field.Enum("resolution_status").Values("resolved", "unresolved", "ambiguous", "unsupported").Default("unresolved").Comment("是否已解析成唯一系統 user"),
		field.String("reason").Optional().Comment("解析理由或無法解析原因，供 debug/audit 使用"),
	}
}

// Edges of the TodoCandidateAssignee.
func (TodoCandidateAssignee) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("candidate", TodoCandidate.Type).Ref("candidate_assignees").Field("candidate_id").Required().Immutable().Unique().Comment("此 assignee 所屬 Todo candidate"),
		edge.To("source_message_mention", ChannelMessageMention.Type).Unique().Field("source_message_mention_id").Comment("此 assignee 來源的訊息 mention；來源不是 mention 時為空"),
		edge.To("user", User.Type).Unique().Field("user_id").Comment("此 assignee 解析出的系統使用者；未解析或 bot 可為空"),
	}
}

// Indexes of the TodoCandidateAssignee.
func (TodoCandidateAssignee) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("candidate_id"),
		index.Fields("candidate_id", "source", "platform_user_id"),
		index.Fields("source_message_mention_id"),
		index.Fields("user_id"),
	}
}

// Annotations of the TodoCandidateAssignee.
func (TodoCandidateAssignee) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Todo candidate assignee 解析快照"),
		entgql.QueryField(),
	}
}
