package commandchain

import (
	"context"
	"strings"
	"sync"

	"assistant-api/internal/ent"

	"github.com/google/uuid"
)

// MessageStore 定義判斷指令訊息鍊所需的最小查詢能力。
type MessageStore interface {
	GetMessageByID(ctx context.Context, id uuid.UUID) (*ent.ChannelMessage, error)
	FindMessageByPlatformMessageID(ctx context.Context, channelID uuid.UUID, platformMessageID string) (*ent.ChannelMessage, error)
	// FindLatestActionOperationByMessageID 用於判斷某節點是否已存在可重用指令。
	FindLatestActionOperationByMessageID(ctx context.Context, messageID uuid.UUID) (string, error)
}

// Service 提供跨平台共用的指令訊息鍊判斷。
type Service interface {
	IsCommandChainMessage(ctx context.Context, message *ent.ChannelMessage, mentionedBot bool) (bool, error)
}

type service struct {
	store MessageStore

	// seedByID 記錄「可視為指令鍊起點」的訊息 ID。
	// 起點由 mention bot 的訊息產生，後續沿回覆關聯追溯到該起點都視為在鍊上。
	mu       sync.RWMutex
	seedByID map[uuid.UUID]struct{}
}

// NewService 建立指令訊息鍊判斷服務。
func NewService(store MessageStore) Service {
	if store == nil {
		return nil
	}
	return &service{store: store, seedByID: make(map[uuid.UUID]struct{})}
}

// IsCommandChainMessage 依規則判斷訊息是否在指令訊息鍊上：
// 1) 本訊息提及 bot。
// 2) 回覆到 (1) 或其後續鍊上的訊息。
// 3) 系統訊息透過 triggered_message_id 指回某個鍊上訊息。
// 4) 回覆任一鍊上訊息的後續回覆。
//
// 注意：reply_to_msg_id 與 triggered_message_id 都可能形成可追溯父節點，
// 但前者來自平台互動，後者來自系統送出訊息時的內部落庫關係。
func (s *service) IsCommandChainMessage(ctx context.Context, message *ent.ChannelMessage, mentionedBot bool) (bool, error) {
	if s == nil || s.store == nil || message == nil {
		return false, nil
	}

	// 規則 1：本訊息直接 mention bot，立刻視為在鍊上並註冊 seed。
	if mentionedBot {
		s.markSeed(message.ID)
		return true, nil
	}

	// 快速路徑：若本訊息本身就是 seed，直接命中。
	if s.isSeed(message.ID) {
		return true, nil
	}

	// 新規則：若當前訊息本身已有 action_result 對應 operation，
	// 直接視為在 command 鏈上，避免再做一次昂貴的指令解析。
	if hasCommand, err := s.messageHasCommand(ctx, message.ID); err != nil {
		return false, err
	} else if hasCommand {
		s.markSeed(message.ID)
		return true, nil
	}

	// visited 用於防止資料異常形成回圈（例如 A->B->A）造成無限追溯。
	visited := map[uuid.UUID]struct{}{message.ID: {}}
	current := message

	for {
		// 依優先序解析父訊息：
		// 1) triggered_message_id（系統觸發來源）
		// 2) reply_to_msg_id（平台回覆目標）
		parent, err := s.resolveParent(ctx, current)
		if err != nil {
			// 查詢失敗交給上層處理，不在此吞錯以免掩蓋資料問題。
			return false, err
		}
		if parent == nil {
			// 無父節點代表已追到鏈尾且未命中 seed。
			// 依規則判定不在指令鍊上。
			return false, nil
		}

		if _, ok := visited[parent.ID]; ok {
			// 偵測回圈時保守回傳 false，避免流程卡住。
			return false, nil
		}
		if parent.ID == uuid.Nil {
			// 保護性分支：無效 ID 不再繼續追溯。
			return false, nil
		}
		visited[parent.ID] = struct{}{}

		// 命中任一 seed，代表符合規則 2/3/4（回覆或關聯到鍊上訊息）。
		if s.isSeed(parent.ID) {
			return true, nil
		}
		// 上溯父節點時同樣檢查是否有既有指令，
		// 命中後立刻短路成功，並把該節點註冊為 seed 供後續快取命中。
		if hasCommand, checkErr := s.messageHasCommand(ctx, parent.ID); checkErr != nil {
			return false, checkErr
		} else if hasCommand {
			s.markSeed(parent.ID)
			return true, nil
		}

		current = parent
	}
}

func (s *service) messageHasCommand(ctx context.Context, messageID uuid.UUID) (bool, error) {
	if s == nil || s.store == nil || messageID == uuid.Nil {
		return false, nil
	}
	// 只要 action_results 能解析出非空 api_operation，
	// 就代表這個節點有既有指令可重用。
	operation, err := s.store.FindLatestActionOperationByMessageID(ctx, messageID)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(operation) != "", nil
}

func (s *service) resolveParent(ctx context.Context, message *ent.ChannelMessage) (*ent.ChannelMessage, error) {
	if message == nil {
		return nil, nil
	}

	// 優先讀 triggered_message_id。
	// 這代表系統訊息由某訊息觸發，可直接用內部 ID 取來源訊息。
	if message.TriggeredMessageID != nil && *message.TriggeredMessageID != uuid.Nil {
		return s.store.GetMessageByID(ctx, *message.TriggeredMessageID)
	}

	// 一般使用者 reply 不寫 triggered 關聯，直接以平台回覆 ID 在同 channel 查找。
	// 這能讓「補參數」這類手動回覆延續指令鍊，同時不污染系統觸發語意。
	if replyToMsgID := strings.TrimSpace(message.ReplyToMsgID); replyToMsgID != "" {
		return s.store.FindMessageByPlatformMessageID(ctx, message.ChannelID, replyToMsgID)
	}

	return nil, nil
}

func (s *service) markSeed(id uuid.UUID) {
	if id == uuid.Nil {
		return
	}
	// map 寫入需持有寫鎖，避免併發 panic。
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seedByID[id] = struct{}{}
}

func (s *service) isSeed(id uuid.UUID) bool {
	if id == uuid.Nil {
		return false
	}
	// 讀取用讀鎖，可讓多執行緒併發查詢 seed。
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.seedByID[id]
	return ok
}
