package runtimecontext

import "context"

// contextKey 是 runtimecontext 內部專用的 context key 型別。
//
// 使用自訂型別而不是直接用 string 的原因：
// - 避免和其他 package 也用 context.WithValue 存入的 string key 撞名。
// - 讓這個 package 成為唯一知道實際 key 值的地方，外部只能透過公開函式讀寫。
//
// 注意：
// context.Context 在 Go 裡主要用來傳遞 request-scoped metadata、取消訊號與 deadline。
// 這裡只放「平台執行期必要資訊」，例如 Slack workspace id 或本次 webhook 使用的 bot id；
// 不應把大量業務資料、action 參數、使用者輸入內容塞進 context。
type contextKey string

const (
	// workspaceTeamIDKey 保存多租戶平台的 workspace / team 識別。
	//
	// 目前主要用於 Slack：同一個 Slack app 可以安裝到多個 workspace，
	// 發送訊息時必須知道本次訊息屬於哪個 team，才能查出正確的 bot token。
	// 例如 slack.PushMessageService.SendTextToChat 會從 context 取 team id，
	// 再透過 token store 解析該 workspace 專屬的 xoxb token。
	workspaceTeamIDKey contextKey = "workspace_team_id"

	// botSenderIDKey 保存「本次流程應使用的 bot sender id」。
	//
	// 不同平台或不同 workspace 可能有不同的 bot 身分：
	// - Slack workspace 會有各自的 bot user id。
	// - outbound 訊息落庫時，需要知道 sender_id 應該記成哪個 bot。
	//
	// 這個值是 request-scoped：由 webhook 入口依本次事件解析後放入 context，
	// 下游如 conversationflow / realtime auto-translate 再取出使用。
	botSenderIDKey contextKey = "bot_sender_id"
)

// WithWorkspaceTeamID 將 workspace/team id 寫入 context，回傳新的 context。
//
// 典型使用場景：
// - Slack webhook 收到事件後，從 event payload 的 team_id 取得 workspace id。
// - 呼叫 message pipeline 前把 team id 放進 context。
// - 下游 Slack sender 需要發訊息時，從 context 取出 team id 以解析 workspace bot token。
//
// 行為細節：
// - teamID 為空時直接回傳原 ctx，不寫入空值，避免覆蓋上游已有資訊。
// - ctx 為 nil 時改用 context.Background()，讓呼叫端不用先做 nil 防護。
// - context.WithValue 不會修改原 ctx，而是建立帶有該值的新 context；呼叫端必須使用回傳值。
func WithWorkspaceTeamID(ctx context.Context, teamID string) context.Context {
	if teamID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, workspaceTeamIDKey, teamID)
}

// WorkspaceTeamIDFromContext 從 context 讀出 workspace/team id。
//
// 回傳空字串代表：
// - ctx 為 nil。
// - 上游沒有呼叫 WithWorkspaceTeamID。
// - context 內的值型別不是 string。
//
// 呼叫端通常會把空字串視為「缺少 workspace」，再依自己的責任決定：
// - Slack sender 需要 token 時會回錯，因為缺 team id 無法解析 token。
// - 非多租戶平台可忽略此值。
func WorkspaceTeamIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	teamID, _ := ctx.Value(workspaceTeamIDKey).(string)
	return teamID
}

// WithBotSenderID 將本次流程使用的 bot sender id 寫入 context，回傳新的 context。
//
// 典型使用場景：
// - Slack webhook 先依 team id 查出該 workspace 的 bot user id。
// - 將 bot user id 放進 context 後交給 message pipeline。
// - 下游發送成功通知或自動翻譯訊息並落庫時，用這個 id 當 outbound sender_id。
//
// 為什麼不用全域設定就好：
// - LINE 這類平台可能只有一組固定 bot id。
// - Slack 是 workspace-scoped，不同 workspace 的 bot user id 可能不同。
// - 因此 bot sender id 必須跟著單次 webhook/request 往下傳。
//
// 行為細節與 WithWorkspaceTeamID 相同：空字串不寫入、nil ctx 會補 Background。
func WithBotSenderID(ctx context.Context, botSenderID string) context.Context {
	if botSenderID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, botSenderIDKey, botSenderID)
}

// BotSenderIDFromContext 從 context 讀出本次流程使用的 bot sender id。
//
// 下游使用方式：
// - conversationflow 送出 action 成功通知時，優先使用 context 內的 bot id。
// - realtime auto-translate 落庫 outbound 翻譯訊息時，優先使用 context 內的 bot id。
// - 若 context 沒有值，呼叫端通常會再 fallback 到建構 service 時注入的預設 bot id。
//
// 這個函式刻意只回傳 string，不回傳 error：
// - context metadata 缺失不一定是錯誤，可能是平台不需要。
// - 是否允許缺值，由真正需要 bot id 的下游流程自行決定。
func BotSenderIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	botSenderID, _ := ctx.Value(botSenderIDKey).(string)
	return botSenderID
}
