package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SlackWorkspace holds the schema definition for a Slack workspace installation.
type SlackWorkspace struct {
	ent.Schema
}

// Mixin of the SlackWorkspace.
func (SlackWorkspace) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the SlackWorkspace.
func (SlackWorkspace) Fields() []ent.Field {
	return []ent.Field{
		field.String("app_id").NotEmpty().Comment("Slack app ID"),
		field.String("platform_team_id").NotEmpty().Comment("Slack workspace/team ID"),
		field.String("team_name").Optional().Nillable().Comment("Slack workspace/team display name"),
		field.String("bot_token").NotEmpty().Sensitive().Comment("Slack bot access token for this workspace"),
		field.String("bot_user_id").Optional().Nillable().Comment("Slack bot user ID for this workspace"),
	}
}

// Indexes of the SlackWorkspace.
func (SlackWorkspace) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("app_id", "platform_team_id").Unique(),
	}
}

// Annotations of the SlackWorkspace.
func (SlackWorkspace) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Slack workspace installation"),
	}
}
