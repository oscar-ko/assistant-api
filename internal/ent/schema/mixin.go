package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/mixin"
	"github.com/google/uuid"
)

// IdMixin 提供通用 UUID 主鍵欄位。
type IdMixin struct {
	mixin.Schema
}

// Fields 回傳 UUID 型別 id。
func (IdMixin) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("id", uuid.UUID{}).Default(uuid.New).Immutable().Comment("全域唯一主鍵 UUID"),
	}
}

// TimeMixin 提供通用建立/更新時間欄位。
type TimeMixin struct {
	mixin.Schema
}

// Fields 回傳 created_at 與 updated_at。
func (TimeMixin) Fields() []ent.Field {
	return []ent.Field{
		field.Time("created_at").Immutable().Default(time.Now).Comment("建立時間"),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now).Comment("最後更新時間"),
	}
}
