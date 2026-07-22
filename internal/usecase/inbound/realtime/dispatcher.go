package realtime

import (
	"context"
	"strings"
	"sync"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"

	"github.com/google/uuid"
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
//
// dispatcher 也會用 channel key 做單一 process 內的序列化：
//   - 同一個 channel 的非指令 realtime 流程會排隊執行，避免後進訊息先完成
//     classification 或 Todo Reminder context analysis，讓依賴近期訊息順序的服務看到錯亂狀態。
//   - 不同 channel 使用不同 mutex，所以某個頻道的 LLM 或外部 API 較慢時，不會阻塞其他頻道。
//   - 這裡不提供跨 process 的全域順序；若未來水平擴展多個 API instance，應改由 queue、
//     DB advisory lock 或分散式鎖承擔跨 instance 的同頻道順序保證。
type Dispatcher struct {
	services []NonCommandService
	// channelLocksMu 只保護 channelLocks map 的建立與查找，不包住實際 service 執行。
	// 實際等待點在每個 channel 自己的 mutex，這樣不同 channel 仍能並行處理。
	channelLocksMu sync.Mutex
	// channelLocks 優先用已落庫訊息的內部 channel UUID 當 key；缺少 SavedMessage 時才退到平台 channel id。
	// map 目前不主動清理，因為 dispatcher 生命週期通常等同 webhook service，channel 數量也受綁定資料控制。
	// 若未來支援大量短生命週期 channel，再補 TTL 或 LRU 清理會比在這裡預先複雜化更合適。
	channelLocks map[string]*sync.Mutex
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
	return &Dispatcher{services: filtered, channelLocks: make(map[string]*sync.Mutex)}
}

// Handle 依註冊順序執行所有非指令即時服務。
//
// 行為說明：
// 1) dispatcher 或 message 為空時直接返回，避免空指標。
// 2) ctx 為 nil 時補 context.Background()，確保下游不需重複防禦。
// 3) 先正規化平台欄位（trim），統一下游比較與查詢輸入。
// 4) 依 channel key 取得鎖，讓同一頻道內多則訊息的 realtime side-effect 按進站順序完成。
// 5) 逐一呼叫 service，讓每個服務自行決定是否處理此訊息。
func (d *Dispatcher) Handle(ctx context.Context, messageCtx MessageContext) {
	if d == nil || messageCtx.Message == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	messageCtx.PlatformUserID = strings.TrimSpace(messageCtx.PlatformUserID)
	messageCtx.QuoteRef = strings.TrimSpace(messageCtx.QuoteRef)
	// 鎖放在欄位正規化之後、service fan-out 之前，讓同一則訊息的所有 realtime service
	// 成為不可交錯的處理單元；這對會讀取最近訊息窗口的 Todo Reminder 尤其重要。
	unlock := d.lockMessageChannel(messageCtx)
	defer unlock()
	for _, service := range d.services {
		service.Handle(ctx, messageCtx)
	}
}

// lockMessageChannel 回傳目前訊息所屬 channel 的 unlock function。
//
// key 選擇順序刻意以 SavedMessage.ChannelID 優先：
//   - SavedMessage.ChannelID 是資料庫內部 UUID，跨 LINE/Slack 等 provider 都有一致語意。
//   - Message.ChannelID 是平台外部 id，只在訊息尚未落庫或測試情境下作為次佳識別。
//
// 若完全拿不到 channel key，函式會回傳 no-op unlock，讓 dispatcher 維持 fail-fast、
// 不補隱性狀態的風格。呼叫端可以一律 defer unlock()，不需要分支處理有沒有鎖到 channel。
func (d *Dispatcher) lockMessageChannel(messageCtx MessageContext) func() {
	if d == nil {
		return func() {}
	}
	key := ""
	if messageCtx.SavedMessage != nil && messageCtx.SavedMessage.ChannelID != uuid.Nil {
		key = messageCtx.SavedMessage.ChannelID.String()
	} else if messageCtx.Message != nil {
		// 這個分支主要保留給單元測試或未來不先落庫的 realtime 呼叫點。
		// 正常 webhook pipeline 會先持久化訊息，因此多數情況會走上面的內部 UUID key。
		key = strings.TrimSpace(messageCtx.Message.ChannelID)
	}
	if key == "" {
		return func() {}
	}
	d.channelLocksMu.Lock()
	if d.channelLocks == nil {
		// 防禦零值 Dispatcher。NewDispatcher 會初始化 map，但測試或手動組 struct 時仍可能留下 nil。
		d.channelLocks = make(map[string]*sync.Mutex)
	}
	lock := d.channelLocks[key]
	if lock == nil {
		// 每個 channel 只建立一把 mutex；同 channel 訊息會排在同一把鎖後面，
		// 不同 channel 則拿不同鎖，因此不會把整個 realtime dispatcher 變成全域單線程。
		lock = &sync.Mutex{}
		d.channelLocks[key] = lock
	}
	d.channelLocksMu.Unlock()
	lock.Lock()
	return lock.Unlock
}
