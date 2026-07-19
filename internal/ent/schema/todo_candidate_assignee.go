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

// TodoCandidateAssignee 保存 Todo candidate 的 assignee evidence。
//
// mention 來源只保存 source_message_mention_id，平台 ID、顯示文字、bot flag 與 mention 解析狀態都回頭讀
// channel_message_mentions，避免把同一份訊息事實複製到 Todo domain 表。非 mention 來源才保存
// assignee_text / resolved_user_id / resolution_status 這些 Todo 判斷欄位。
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
		field.UUID("resolved_user_id", uuid.UUID{}).Optional().Nillable().Comment("非 mention 來源解析出的系統 user ID；mention 來源請讀 source_message_mention.user_id"),
		field.Enum("source").Values("mention", "analyzer", "sender", "reply_context").Default("mention").Comment("assignee 來源：structured mention、模型字面、sender 或 reply context"),
		field.String("assignee_text").Optional().Comment("非 mention 來源的 assignee 原文，例如模型抽取的人名；mention 來源請讀 source_message_mention.display_text"),
		field.Enum("resolution_status").Values("resolved", "unresolved", "ambiguous", "unsupported").Optional().Nillable().Comment("非 mention 來源是否已解析成唯一系統 user；mention 來源請讀 source_message_mention.resolution_status"),
		field.String("reason").Optional().Comment("解析理由或無法解析原因，供 debug/audit 使用"),
	}
}

// Edges of the TodoCandidateAssignee.
func (TodoCandidateAssignee) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("candidate", TodoCandidate.Type).Ref("candidate_assignees").Field("candidate_id").Required().Immutable().Unique().Comment("此 assignee 所屬 Todo candidate"),
		edge.To("source_message_mention", ChannelMessageMention.Type).Unique().Field("source_message_mention_id").Comment("此 assignee 來源的訊息 mention；來源不是 mention 時為空"),
		edge.To("resolved_user", User.Type).Unique().Field("resolved_user_id").Comment("非 mention 來源解析出的系統使用者；mention 來源請讀 source_message_mention.user"),
	}
}

// Indexes of the TodoCandidateAssignee.
func (TodoCandidateAssignee) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("candidate_id"),
		index.Fields("candidate_id", "source", "assignee_text"),
		index.Fields("source_message_mention_id"),
		index.Fields("resolved_user_id"),
	}
}

// Annotations of the TodoCandidateAssignee.
func (TodoCandidateAssignee) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Todo candidate assignee evidence"),
		entgql.QueryField(),
	}
}
