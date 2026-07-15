package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// User holds the schema definition for the User entity.
type User struct {
	ent.Schema
}

// Mixin of the User.
func (User) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
	}
}

// Fields of the User.
func (User) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().Comment("使用者顯示名稱").Annotations(entgql.OrderField("NAME")),
		field.String("email").NotEmpty().Unique().Comment("使用者唯一電子郵件").Annotations(entgql.OrderField("EMAIL")),
	}
}

// Annotations of the User.
func (User) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("系統使用者"),
		entgql.QueryField(),
		entgql.Mutations(entgql.MutationCreate(), entgql.MutationUpdate()),
	}
}

// Edges of the User.
func (User) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("line", Line.Type).
			Ref("user").
			Comment("使用者綁定的 LINE 帳號清單（可多筆）"),
		edge.From("channel_service_members", ChannelServiceMember.Type).
			Ref("user").
			Comment("使用者啟用服務的頻道成員設定"),
		edge.From("owned_translation_locales", TranslationLocale.Type).
			Ref("owner").
			Comment("使用者新增的翻譯目標語言設定"),
	}
}
