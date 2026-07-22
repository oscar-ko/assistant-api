package channellifecycle

import (
	"context"
	"fmt"
	"strings"

	"assistant-api/internal/ent"
)

// ChannelRepository 是 channel lifecycle usecase 需要的最小資料存取契約。
// Slack/LINE 各自負責判斷「外部事件是否代表 bot 進入或離開對話空間」，
// 這層只集中處理我們系統 channel 的建立、重新啟用與停用規則。
type ChannelRepository interface {
	GetOrCreateChannel(ctx context.Context, platform string, groupID string, channelType string, channelName string) (*ent.Channel, error)
	SetChannelActiveByPlatformGroupID(ctx context.Context, platform string, groupID string, isActive bool) error
}

type Service struct {
	repo ChannelRepository
}

type JoinInput struct {
	Platform          string
	PlatformChannelID string
	ChannelType       string
	ChannelName       string
}

func NewService(repo ChannelRepository) Service {
	return Service{repo: repo}
}

// Join 建立或啟用外部群組/頻道對應的系統 channel。
// 這裡刻意不解析 Slack/LINE 原始事件，也不決定 channel name；
// provider 層先完成平台差異處理後，再把正規化結果交給這個 usecase。
func (s Service) Join(ctx context.Context, input JoinInput) (*ent.Channel, error) {
	if s.repo == nil {
		return nil, fmt.Errorf("channel lifecycle repository not initialized")
	}
	platform := strings.TrimSpace(input.Platform)
	platformChannelID := strings.TrimSpace(input.PlatformChannelID)
	channelType := strings.TrimSpace(input.ChannelType)
	if channelType == "" {
		channelType = "group"
	}

	channelItem, err := s.repo.GetOrCreateChannel(ctx, platform, platformChannelID, channelType, strings.TrimSpace(input.ChannelName))
	if err != nil {
		return nil, err
	}
	if err := s.repo.SetChannelActiveByPlatformGroupID(ctx, platform, platformChannelID, true); err != nil {
		return nil, err
	}
	return channelItem, nil
}

// Leave 停用外部群組/頻道對應的系統 channel。
// 嚴格模式下，channel 不存在會由 repository 回錯；這可避免 provider 把未建立來源默默視為成功。
func (s Service) Leave(ctx context.Context, platform string, platformChannelID string) error {
	if s.repo == nil {
		return fmt.Errorf("channel lifecycle repository not initialized")
	}
	return s.repo.SetChannelActiveByPlatformGroupID(ctx, strings.TrimSpace(platform), strings.TrimSpace(platformChannelID), false)
}