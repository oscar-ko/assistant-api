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

// Action stores operations available for a skill.
type Action struct {
	ent.Schema
}

func (Action) Mixin() []ent.Mixin {
	return []ent.Mixin{IdMixin{}}
}

func (Action) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("skill_id", uuid.UUID{}).Immutable().Comment("所屬 skill"),
		field.Enum("action_code").Values("enable", "disable", "configure", "query_status").Comment("動作代碼"),
		field.String("name").NotEmpty().Comment("動作名稱"),
		field.String("description").Optional().Nillable().Comment("動作描述").
			SchemaType(map[string]string{"mysql": "TEXT", "postgres": "text"}),
		field.String("api_operation").NotEmpty().Comment("API operation 名稱"),
		field.String("command_purpose").Optional().Nillable().Comment("此動作的目的說明，供 AI 理解提問目標").
			SchemaType(map[string]string{"mysql": "TEXT", "postgres": "text"}),
	}
}

func (Action) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("skill_id", "action_code").Unique(),
		index.Fields("api_operation"),
	}
}

func (Action) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("skill", Skill.Type).Ref("actions").Field("skill_id").Required().Immutable().Unique(),
		edge.To("routes", ActionRoute.Type),
	}
}

func (Action) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("技能動作"),
		entgql.QueryField(),
	}
}
