package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
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
		field.String("line_user_id").NotEmpty().Unique().Comment("LINE 平台使用者 ID"),
		field.String("display_name").Optional().Nillable().Comment("LINE 顯示名稱"),
		field.String("email").Optional().Nillable().Comment("LINE 帳號電子郵件（可能為空）"),
		field.String("picture").Optional().Nillable().Comment("LINE 大頭貼 URL"),
	}
}

// Edges of the Line.
func (Line) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).
			Unique().
			Required().
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
