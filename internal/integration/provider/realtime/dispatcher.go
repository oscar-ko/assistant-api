package realtime

import (
	"context"
	"strings"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"
)

// MessageContext 描述「非指令即時服務」執行時所需的最小上下文。
//
// 設計意圖：
//  1. 避免把 provider webhook 的完整事件結構直接暴露給共用服務層，
//     讓 Slack/LINE/WhatsApp 只需做一次 adapter 即可重用同一套服務。
//  2. 保留可追溯所需欄位（原始訊息、已落庫訊息、平台使用者、引用資訊），
//     讓 side-effect 服務（翻譯、摘要、提醒）不必回頭耦合 webhook 細節。
//  3. 盡量維持資料結構扁平，降低新增服務時的閱讀與接線成本。
type MessageContext struct {
	Message        *unifiedmessage.Message
	SavedMessage   *ent.ChannelMessage
	PlatformUserID string
	QuoteRef       string
}

// NonCommandService 定義非指令訊息的即時服務介面。
//
// 介面刻意只有一個 Handle：
// - 輸入使用統一 MessageContext，避免每個服務自定參數造成接線碎片化。
// - 服務本身採 fail-fast：若條件不符直接 return，不應回傳錯誤中斷整體流程。
// - 真正的可觀測性交由服務內 logger 處理，dispatcher 不負責判斷業務錯誤。
type NonCommandService interface {
	Handle(ctx context.Context, messageCtx MessageContext)
}

// Dispatcher 負責順序分派多個非指令即時服務。
//
// 執行模型採「同 goroutine 順序呼叫」：
//   - 優點：流程可預期、除錯簡單、log 事件順序穩定。
//   - 取捨：若某服務較慢，會拖慢後續服務；目前先求可維護性，
//     後續若有性能需求可改為 fan-out 並行版本。
type Dispatcher struct {
	services []NonCommandService
}

// NewDispatcher 建立非指令服務分派器。
//
// 注意：
// - 會過濾 nil service，避免執行期 panic。
// - 不做去重，因為同一服務型別在不同設定下可能需要多次註冊。
func NewDispatcher(services ...NonCommandService) *Dispatcher {
	filtered := make([]NonCommandService, 0, len(services))
	for _, service := range services {
		if service == nil {
			continue
		}
		filtered = append(filtered, service)
	}
	return &Dispatcher{services: filtered}
}

// Handle 依註冊順序執行所有非指令即時服務。
//
// 行為說明：
// 1) dispatcher 或 message 為空時直接返回，避免空指標。
// 2) ctx 為 nil 時補 context.Background()，確保下游不需重複防禦。
// 3) 先正規化平台欄位（trim），統一下游比較與查詢輸入。
// 4) 逐一呼叫 service，讓每個服務自行決定是否處理此訊息。
func (d *Dispatcher) Handle(ctx context.Context, messageCtx MessageContext) {
	if d == nil || messageCtx.Message == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	messageCtx.PlatformUserID = strings.TrimSpace(messageCtx.PlatformUserID)
	messageCtx.QuoteRef = strings.TrimSpace(messageCtx.QuoteRef)
	for _, service := range d.services {
		service.Handle(ctx, messageCtx)
	}
}
