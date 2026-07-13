package schema

import (
	"entgo.io/contrib/entgql"
	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

// ActionRoute stores localized routing text used by vector retrieval.
type ActionRoute struct {
	ent.Schema
}

func (ActionRoute) Mixin() []ent.Mixin {
	return []ent.Mixin{IdMixin{}}
}

func (ActionRoute) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("action_id", uuid.UUID{}).Immutable().Comment("所屬 action"),
		field.String("route_text").NotEmpty().Comment("給 RAG 檢索的文字").
			SchemaType(map[string]string{"mysql": "TEXT", "postgres": "text"}),
		field.Other("embedding", pgvector.Vector{}).
			Optional().
			Nillable().
			Comment("向量嵌入（PostgreSQL pgvector）").
			SchemaType(map[string]string{dialect.Postgres: "vector(512)"}),
		field.String("locale").NotEmpty().Comment("語系，例如 zh-TW, en, ja"),
	}
}

func (ActionRoute) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("action_id", "locale"),
		index.Fields("locale"),
		index.Fields("embedding").
			Annotations(
				entsql.IndexType("hnsw"),
				entsql.OpClass("vector_l2_ops"),
			),
	}
}

func (ActionRoute) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("action", Action.Type).Ref("routes").Field("action_id").Required().Immutable().Unique(),
	}
}

func (ActionRoute) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("技能路由文字"),
		entgql.QueryField(),
	}
}
