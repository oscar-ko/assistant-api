package channellifecycle

import (
	"context"
	"testing"

	"assistant-api/internal/ent"

	"github.com/google/uuid"
)

type fakeChannelRepository struct {
	// fake repo 只記錄 usecase 傳給 repository 的正規化參數，
	// 讓測試聚焦在 Join/Leave 是否維持「建立後啟用」與「離開只停用」的應用規則。
	createdPlatform    string
	createdGroupID     string
	createdChannelType string
	createdChannelName string
	activePlatform     string
	activeGroupID      string
	activeValue        bool
	activeCalled       bool
	channel            *ent.Channel
}

func (r *fakeChannelRepository) GetOrCreateChannel(ctx context.Context, platform string, groupID string, channelType string, channelName string) (*ent.Channel, error) {
	r.createdPlatform = platform
	r.createdGroupID = groupID
	r.createdChannelType = channelType
	r.createdChannelName = channelName
	if r.channel == nil {
		r.channel = &ent.Channel{ID: uuid.New()}
	}
	return r.channel, nil
}

func (r *fakeChannelRepository) SetChannelActiveByPlatformGroupID(ctx context.Context, platform string, groupID string, isActive bool) error {
	r.activePlatform = platform
	r.activeGroupID = groupID
	r.activeValue = isActive
	r.activeCalled = true
	return nil
}

func TestServiceJoinCreatesAndActivatesChannel(t *testing.T) {
	// provider 已經解析完成平台差異後，Join 只負責把系統 channel 建立並設為 active。
	repo := &fakeChannelRepository{}
	channelItem, err := NewService(repo).Join(context.Background(), JoinInput{
		Platform:          " slack ",
		PlatformChannelID: " C123 ",
		ChannelName:       " general ",
	})
	if err != nil {
		t.Fatalf("join channel: %v", err)
	}
	if channelItem == nil || channelItem.ID == uuid.Nil {
		t.Fatalf("expected created channel item")
	}
	if repo.createdPlatform != "slack" || repo.createdGroupID != "C123" || repo.createdChannelType != "group" || repo.createdChannelName != "general" {
		t.Fatalf("unexpected create input: platform=%q group=%q type=%q name=%q", repo.createdPlatform, repo.createdGroupID, repo.createdChannelType, repo.createdChannelName)
	}
	if !repo.activeCalled || repo.activePlatform != "slack" || repo.activeGroupID != "C123" || !repo.activeValue {
		t.Fatalf("expected channel to be activated, got called=%v platform=%q group=%q active=%v", repo.activeCalled, repo.activePlatform, repo.activeGroupID, repo.activeValue)
	}
}

func TestServiceLeaveDeactivatesChannel(t *testing.T) {
	// Leave 不建立 channel；不存在與否由 repository 嚴格回錯，這裡只驗證共用層傳遞 deactivate 語意。
	repo := &fakeChannelRepository{}
	if err := NewService(repo).Leave(context.Background(), " line ", " G123 "); err != nil {
		t.Fatalf("leave channel: %v", err)
	}
	if !repo.activeCalled || repo.activePlatform != "line" || repo.activeGroupID != "G123" || repo.activeValue {
		t.Fatalf("expected channel to be deactivated, got called=%v platform=%q group=%q active=%v", repo.activeCalled, repo.activePlatform, repo.activeGroupID, repo.activeValue)
	}
}