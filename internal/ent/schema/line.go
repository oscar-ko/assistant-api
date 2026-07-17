package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"github.com/google/uuid"
)

// Line holds the schema definition for the Line entity.
type Line struct {
	ent.Schema
}

// Mixin of the Line.
func (Line) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
	}
}

// Fields of the Line.
func (Line) Fields() []ent.Field {
	return []ent.Field{
		field.String("platform_user_id").NotEmpty().Unique().Comment("LINE 平台使用者 ID"),
		field.String("display_name").Optional().Nillable().Comment("LINE 顯示名稱"),
		field.String("picture").Optional().Nillable().Comment("LINE 大頭貼 URL"),
		field.UUID("user_id", uuid.UUID{}).Comment("對應系統內 user_id"),
	}
}

// Edges of the Line.
func (Line) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).
			Unique().
			Required().
			Field("user_id").
			Comment("LINE 帳號對應的系統使用者"),
	}
}

// Annotations of the Line.
func (Line) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("LINE 綁定資訊"),
		entgql.QueryField(),
	}
}
