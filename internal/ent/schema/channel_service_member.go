package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// ChannelServiceMember 記錄「哪個系統使用者」在「哪個頻道」啟用了「哪個服務技能」。
//
// 重要資料邊界：
// - 這張表只保存 internal user_id，不保存 LINE/Slack platform_user_id。
// - 平台帳號綁定屬於 lines/slacks 等 provider binding table 的責任。
// - 即時服務要先把 platform_user_id 解析成 internal user_id，再查這張表做啟用判斷。
type ChannelServiceMember struct {
	ent.Schema
}

// Mixin of the ChannelServiceMember.
func (ChannelServiceMember) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the ChannelServiceMember.
func (ChannelServiceMember) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("channel_id", uuid.UUID{}),
		field.UUID("user_id", uuid.UUID{}),
		field.UUID("skill_id", uuid.UUID{}).Comment("服務技能識別（對應 skills.id）"),
	}
}

// Indexes of the ChannelServiceMember.
func (ChannelServiceMember) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel_id", "user_id", "skill_id").Unique(),
		index.Fields("channel_id", "skill_id"),
		index.Fields("user_id", "skill_id"),
	}
}

// Edges of the ChannelServiceMember.
func (ChannelServiceMember) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("channel", Channel.Type).
			Field("channel_id").
			Required().
			Unique().
			Comment("service 所屬頻道"),
		edge.To("user", User.Type).
			Field("user_id").
			Required().
			Unique().
			Comment("啟用 service 的成員"),
		edge.To("skill", Skill.Type).
			Field("skill_id").
			Required().
			Unique().
			Comment("對應的服務技能"),
	}
}
