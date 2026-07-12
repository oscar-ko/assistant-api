package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
)

// Line holds the schema definition for the Line entity.
type Line struct {
	ent.Schema
}

// Fields of the Line.
func (Line) Fields() []ent.Field {
	return []ent.Field{
		field.String("line_user_id").NotEmpty().Unique(),
		field.String("display_name").Optional().Nillable(),
		field.String("email").Optional().Nillable(),
		field.String("picture").Optional().Nillable(),
	}
}

// Edges of the Line.
func (Line) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("user", User.Type).
			Unique().
			Required(),
	}
}
