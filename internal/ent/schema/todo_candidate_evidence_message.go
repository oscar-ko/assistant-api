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

// TodoCandidateEvidenceMessage 保存 Todo candidate 和訊息之間的語意 evidence 關聯。
//
// 這張表不是 prompt cache，也不是正式 reminder 狀態；它只保存「某則訊息本身被判定與某個
// candidate 有直接追蹤關係」的 anchor。之後組 prompt 時，可以用這些 anchor 往前後抓小訊息窗，
// 避免長討論串因固定 recent window 太短而失去上下文。
type TodoCandidateEvidenceMessage struct {
	ent.Schema
}

// Mixin of the TodoCandidateEvidenceMessage.
func (TodoCandidateEvidenceMessage) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the TodoCandidateEvidenceMessage.
func (TodoCandidateEvidenceMessage) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("channel_id", uuid.UUID{}).Immutable().Comment("evidence 所屬 channel ID，方便同 channel 召回與索引"),
		field.UUID("candidate_id", uuid.UUID{}).Immutable().Comment("此 evidence 關聯的 Todo candidate ID"),
		field.UUID("message_id", uuid.UUID{}).Immutable().Comment("作為 candidate 語意 anchor 的 channel_message ID"),
		field.Enum("relation_type").Values("source", "linked", "update", "clarification", "acknowledgement", "cancellation", "related_context", "explicit_reply", "ambiguous").Comment("訊息和 candidate 的語意關係"),
		field.Enum("source").Values("analyzer", "explicit_reply", "thread", "system", "user_confirmation").Default("analyzer").Comment("此 evidence 關聯的產生來源"),
		field.Float("confidence").Default(0).Comment("建立關聯時的信心分數；通常沿用 todo analyzer confidence"),
		field.String("reason").Optional().Comment("建立關聯的簡短理由，供 debug/audit 使用"),
		field.Bool("is_active").Default(true).Comment("是否仍作為召回 candidate context 的活躍 evidence"),
	}
}

// Edges of the TodoCandidateEvidenceMessage.
func (TodoCandidateEvidenceMessage) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("candidate", TodoCandidate.Type).Ref("evidence_messages").Field("candidate_id").Required().Immutable().Unique().Comment("此 evidence 所屬 Todo candidate"),
		edge.To("channel", Channel.Type).Field("channel_id").Required().Unique().Immutable().Comment("此 evidence 所屬 channel"),
		edge.To("message", ChannelMessage.Type).Field("message_id").Required().Unique().Immutable().Comment("作為 evidence anchor 的訊息"),
	}
}

// Indexes of the TodoCandidateEvidenceMessage.
func (TodoCandidateEvidenceMessage) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel_id", "is_active"),
		index.Fields("candidate_id", "is_active"),
		index.Fields("message_id"),
		index.Fields("candidate_id", "message_id", "relation_type").Unique(),
	}
}

// Annotations of the TodoCandidateEvidenceMessage.
func (TodoCandidateEvidenceMessage) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Todo candidate evidence messages"),
		entgql.QueryField(),
	}
}
