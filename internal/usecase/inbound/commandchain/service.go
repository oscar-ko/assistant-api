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
// 3) 手動回覆但已建立 related_message_id 關聯。
// 4) 回覆任一鍊上訊息的後續回覆。
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

	// visited 用於防止資料異常形成回圈（例如 A->B->A）造成無限追溯。
	visited := map[uuid.UUID]struct{}{message.ID: {}}
	current := message

	for {
		// 依優先序解析父訊息：
		// 1) related_message_id（內部關聯，準確度最高）
		// 2) reply_to_msg_id（平台回覆 ID，作為 fallback）
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

		current = parent
	}
}

func (s *service) resolveParent(ctx context.Context, message *ent.ChannelMessage) (*ent.ChannelMessage, error) {
	if message == nil {
		return nil, nil
	}

	// 優先讀 related_message_id。
	// 這代表系統已完成關聯映射，可直接用內部 ID 取父訊息。
	if message.RelatedMessageID != nil && *message.RelatedMessageID != uuid.Nil {
		return s.store.GetMessageByID(ctx, *message.RelatedMessageID)
	}

	// 若尚未建立 related 關聯，退回以平台回覆 ID 在同 channel 查找。
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
