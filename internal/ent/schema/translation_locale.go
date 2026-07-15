package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
)

// TranslationLocale stores translation target locales under a skill.
type TranslationLocale struct {
	ent.Schema
}

// Mixin of the TranslationLocale.
func (TranslationLocale) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the TranslationLocale.
func (TranslationLocale) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("channel_id", uuid.UUID{}).Comment("所屬頻道 ID"),
		field.UUID("skill_id", uuid.UUID{}).Comment("對應技能 ID"),
		field.UUID("owner_user_id", uuid.UUID{}).Comment("新增該 locale 的使用者 ID"),
		field.String("target_locale").NotEmpty().Comment("翻譯目標語言，例如 en-US, zh-TW"),
	}
}

// Indexes of the TranslationLocale.
func (TranslationLocale) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel_id", "target_locale").Unique(),
		index.Fields("channel_id", "skill_id"),
		index.Fields("skill_id", "target_locale"),
		index.Fields("owner_user_id"),
		index.Fields("target_locale"),
	}
}

// Edges of the TranslationLocale.
func (TranslationLocale) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("channel", Channel.Type).
			Field("channel_id").
			Required().
			Unique().
			Comment("locale 所屬頻道"),
		edge.To("skill", Skill.Type).
			Field("skill_id").
			Required().
			Unique().
			Comment("參數所屬技能"),
		edge.To("owner", User.Type).
			Field("owner_user_id").
			Required().
			Unique().
			Comment("新增該 locale 的擁有者"),
	}
}
