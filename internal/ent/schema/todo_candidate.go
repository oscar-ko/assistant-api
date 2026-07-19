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

// TodoCandidate 儲存 Todo Reminder 從對話中抽出的候選待辦最新狀態。
//
// 這張表刻意先存「候選」而不是正式 reminder：
// - structured analyzer 仍可能缺 assignee/due_text 等欄位。
// - update/ack/cancel 需要先能連回前文候選。
// - 正式提醒排程、時間正規化與通知策略可以在 candidate 穩定後再接下一層狀態機。
type TodoCandidate struct {
	ent.Schema
}

// Mixin of the TodoCandidate.
func (TodoCandidate) Mixin() []ent.Mixin {
	return []ent.Mixin{
		IdMixin{},
		TimeMixin{},
	}
}

// Fields of the TodoCandidate.
func (TodoCandidate) Fields() []ent.Field {
	return []ent.Field{
		field.UUID("channel_id", uuid.UUID{}).Immutable().Comment("候選待辦所屬 channel ID"),
		field.UUID("source_message_id", uuid.UUID{}).Immutable().Comment("建立此候選待辦的第一則訊息 ID"),
		field.UUID("last_message_id", uuid.UUID{}).Comment("最近一次更新此候選待辦的訊息 ID"),
		field.UUID("linked_message_id", uuid.UUID{}).Optional().Nillable().Comment("模型判斷目前訊息接續的歷史訊息 ID；全新候選可為空"),
		field.Enum("status").Values("candidate", "needs_more_info", "acknowledged", "cancelled").Default("candidate").Comment("候選待辦目前狀態；尚未等同正式提醒"),
		field.Enum("last_decision").Values("create_candidate", "update_candidate", "acknowledge", "cancel_candidate", "needs_more_info").Comment("最近一次 todo analyzer decision；no_action 不落庫"),
		field.String("summary").Optional().Comment("整理後的待辦摘要；needs_more_info 時可能暫時為空"),
		field.JSON("assignees", []string{}).Optional().Comment("模型抽取出的負責人或承接者字面名稱"),
		field.String("due_text").Optional().Comment("使用者原文中的時間字面，尚未做日期正規化"),
		field.Time("due_at").Optional().Nillable().Comment("由 due_text 正規化後的提醒時間；只有時間 normalizer 判斷 normalized 時才寫入"),
		field.String("due_timezone").Optional().Comment("due_at 使用的 IANA timezone，例如 Asia/Taipei"),
		field.Enum("due_precision").Values("datetime", "date", "relative_window", "unknown").Default("unknown").Comment("時間正規化精度；date/relative_window 代表可用但仍可能需要產品層確認"),
		field.Enum("due_normalize_decision").Values("normalized", "needs_more_info", "no_due_time").Optional().Nillable().Comment("最近一次時間正規化結果；未執行 normalizer 時為空"),
		field.Float("due_confidence").Default(0).Comment("時間正規化結果的信心分數"),
		field.String("due_reason").Optional().Comment("時間正規化的簡短理由，供 debug/audit 使用"),
		field.JSON("missing_fields", []string{}).Optional().Comment("仍缺少的欄位名稱，例如 summary、assignees、due_text"),
		field.Float("confidence").Default(0).Comment("模型對最近一次 decision 的信心分數"),
		field.String("reason").Optional().Comment("最近一次 decision 的簡短理由，供 debug/audit 使用"),
	}
}

// Edges of the TodoCandidate.
func (TodoCandidate) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("channel", Channel.Type).Field("channel_id").Required().Unique().Immutable().Comment("候選待辦所屬 channel"),
		edge.To("source_message", ChannelMessage.Type).Field("source_message_id").Required().Unique().Immutable().Comment("建立候選待辦的第一則訊息"),
		edge.To("last_message", ChannelMessage.Type).Field("last_message_id").Required().Unique().Comment("最近一次更新候選待辦的訊息"),
		edge.To("linked_message", ChannelMessage.Type).Field("linked_message_id").Unique().Comment("最近一次分析時連結到的歷史訊息"),
	}
}

// Indexes of the TodoCandidate.
func (TodoCandidate) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("channel_id", "status"),
		index.Fields("channel_id", "due_at"),
		index.Fields("channel_id", "source_message_id").Unique(),
		index.Fields("channel_id", "last_message_id"),
	}
}

// Annotations of the TodoCandidate.
func (TodoCandidate) Annotations() []schema.Annotation {
	return []schema.Annotation{
		schema.Comment("Todo Reminder 候選待辦"),
		entgql.QueryField(),
	}
}
