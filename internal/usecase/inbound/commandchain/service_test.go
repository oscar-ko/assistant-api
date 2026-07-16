package commandchain

import (
	"context"
	"testing"

	"assistant-api/internal/ent"

	"github.com/google/uuid"
)

type mockStore struct {
	byID         map[uuid.UUID]*ent.ChannelMessage
	byPlatformID map[string]*ent.ChannelMessage
	actionByMsg  map[uuid.UUID]string
}

func (m mockStore) GetMessageByID(ctx context.Context, id uuid.UUID) (*ent.ChannelMessage, error) {
	_ = ctx
	return m.byID[id], nil
}

func (m mockStore) FindMessageByPlatformMessageID(ctx context.Context, channelID uuid.UUID, platformMessageID string) (*ent.ChannelMessage, error) {
	_ = ctx
	return m.byPlatformID[channelID.String()+"|"+platformMessageID], nil
}

func (m mockStore) FindLatestActionOperationByMessageID(ctx context.Context, messageID uuid.UUID) (string, error) {
	_ = ctx
	return m.actionByMsg[messageID], nil
}

func TestIsCommandChainMessage(t *testing.T) {
	// 建立一條 seed -> child -> grandchild 的訊息鍊，
	// 用來驗證 mention seed、related fallback、reply fallback 三種路徑。
	channelID := uuid.New()
	seedID := uuid.New()
	childID := uuid.New()
	grandchildID := uuid.New()
	unrelatedID := uuid.New()

	seed := &ent.ChannelMessage{ID: seedID, ChannelID: channelID, PlatformMessageID: "m-seed"}
	child := &ent.ChannelMessage{ID: childID, ChannelID: channelID, PlatformMessageID: "m-child", RelatedMessageID: &seedID}
	grandchild := &ent.ChannelMessage{ID: grandchildID, ChannelID: channelID, ReplyToMsgID: "m-child", PlatformMessageID: "m-grand"}
	unrelated := &ent.ChannelMessage{ID: unrelatedID, ChannelID: channelID, PlatformMessageID: "m-other"}

	store := mockStore{
		byID: map[uuid.UUID]*ent.ChannelMessage{
			seedID:       seed,
			childID:      child,
			grandchildID: grandchild,
			unrelatedID:  unrelated,
		},
		byPlatformID: map[string]*ent.ChannelMessage{
			channelID.String() + "|m-seed":  seed,
			channelID.String() + "|m-child": child,
			channelID.String() + "|m-grand": grandchild,
			channelID.String() + "|m-other": unrelated,
		},
		actionByMsg: map[uuid.UUID]string{},
	}

	svc := NewService(store)
	if svc == nil {
		t.Fatal("expected service")
	}

	onChain, err := svc.IsCommandChainMessage(context.Background(), seed, true)
	if err != nil {
		t.Fatalf("seed classify failed: %v", err)
	}
	if !onChain {
		t.Fatal("expected seed message to be on command chain")
	}

	onChain, err = svc.IsCommandChainMessage(context.Background(), child, false)
	if err != nil {
		t.Fatalf("child classify failed: %v", err)
	}
	if !onChain {
		t.Fatal("expected child message to be on command chain")
	}

	onChain, err = svc.IsCommandChainMessage(context.Background(), grandchild, false)
	if err != nil {
		t.Fatalf("grandchild classify failed: %v", err)
	}
	if !onChain {
		t.Fatal("expected grandchild message to be on command chain")
	}

	onChain, err = svc.IsCommandChainMessage(context.Background(), unrelated, false)
	if err != nil {
		t.Fatalf("unrelated classify failed: %v", err)
	}
	if onChain {
		t.Fatal("expected unrelated message not to be on command chain")
	}
}

func TestIsCommandChainMessageHitsAncestorActionResult(t *testing.T) {
	// 模擬「父訊息已有 action_result operation，子訊息是回覆」場景，
	// 期待子訊息可直接判定在 command chain 上。
	channelID := uuid.New()
	parentID := uuid.New()
	childID := uuid.New()

	parent := &ent.ChannelMessage{ID: parentID, ChannelID: channelID, PlatformMessageID: "m-parent"}
	child := &ent.ChannelMessage{ID: childID, ChannelID: channelID, PlatformMessageID: "m-child", RelatedMessageID: &parentID}

	store := mockStore{
		byID: map[uuid.UUID]*ent.ChannelMessage{
			parentID: parent,
			childID:  child,
		},
		byPlatformID: map[string]*ent.ChannelMessage{},
		actionByMsg: map[uuid.UUID]string{
			parentID: "start_translation_locale",
		},
	}

	svc := NewService(store)
	if svc == nil {
		t.Fatal("expected service")
	}

	onChain, err := svc.IsCommandChainMessage(context.Background(), child, false)
	if err != nil {
		t.Fatalf("child classify failed: %v", err)
	}
	if !onChain {
		t.Fatal("expected child message to be on command chain when ancestor has action result")
	}
}
