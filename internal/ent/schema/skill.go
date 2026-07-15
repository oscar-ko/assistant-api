package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Skill stores high-level skill metadata.
type Skill struct {
	ent.Schema
}

func (Skill) Mixin() []ent.Mixin {
	return []ent.Mixin{IdMixin{}}
}

func (Skill) Fields() []ent.Field {
	return []ent.Field{
		field.String("skill_code").NotEmpty().Comment("技能代碼"),
		field.String("name").NotEmpty().Comment("技能名稱"),
		field.String("description").Optional().Nillable().Comment("技能描述").
			SchemaType(map[string]string{"mysql": "TEXT", "postgres": "text"}),
	}
}

func (Skill) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("skill_code").Unique(),
	}
}

func (Skill) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("actions", Action.Type),
		edge.From("channel_service_members", ChannelServiceMember.Type).Ref("skill"),
	}
}

func (Skill) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("技能分類"),
		entgql.QueryField(),
	}
}
