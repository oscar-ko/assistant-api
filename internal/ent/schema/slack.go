package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Slack holds the schema definition for the Slack entity.
type Slack struct {
	ent.Schema
}

// Mixin of the Slack.
func (Slack) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
	}
}

// Fields of the Slack.
func (Slack) Fields() []ent.Field {
	return []ent.Field{
		field.String("team_id").NotEmpty().Comment("Slack workspace/team ID"),
		field.String("slack_user_id").NotEmpty().Comment("Slack 平台使用者 ID"),
		field.String("display_name").Optional().Nillable().Comment("Slack 顯示名稱"),
		field.String("email").Optional().Nillable().Comment("Slack 帳號 email"),
		field.String("picture").Optional().Nillable().Comment("Slack 大頭貼 URL"),
	}
}

// Edges of the Slack.
func (Slack) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).
			Unique().
			Required().
			Comment("Slack 帳號對應的系統使用者"),
	}
}

// Indexes of the Slack.
func (Slack) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("team_id", "slack_user_id").Unique(),
	}
}

// Annotations of the Slack.
func (Slack) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Slack 綁定資訊"),
		entgql.QueryField(),
	}
}
