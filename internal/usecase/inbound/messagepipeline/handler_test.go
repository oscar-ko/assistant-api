package messagepipeline

import (
	"context"
	"testing"

	"assistant-api/internal/ent"
	"assistant-api/internal/integration/unifiedmessage"
	"assistant-api/internal/usecase/inbound/messagepersist"
	"assistant-api/internal/usecase/inbound/realtime"

	"github.com/google/uuid"
)

type pipelineTestStore struct {
	senderUserID *uuid.UUID
}

func (s pipelineTestStore) GetChannelByPlatformGroupID(ctx context.Context, platform string, groupID string) (*ent.Channel, error) {
	_ = ctx
	_ = platform
	_ = groupID
	return &ent.Channel{ID: uuid.New(), GroupID: groupID}, nil
}

func (s pipelineTestStore) UpdateChannelDisplayNameByID(ctx context.Context, channelID uuid.UUID, channelName string) error {
	_ = ctx
	_ = channelID
	_ = channelName
	return nil
}

func (s pipelineTestStore) SaveReceivedMessage(ctx context.Context, channelID uuid.UUID, platform string, platformTenantID string, senderID string, senderName string, platformMessageID string, replyToMsgID string, content string, messageType string, platformTimestamp int64) (*ent.ChannelMessage, error) {
	_ = ctx
	_ = platform
	_ = platformTenantID
	_ = senderName
	_ = platformTimestamp
	return &ent.ChannelMessage{
		ID:                uuid.New(),
		ChannelID:         channelID,
		SenderID:          senderID,
		SenderUserID:      s.senderUserID,
		PlatformMessageID: platformMessageID,
		ReplyToMsgID:      replyToMsgID,
		Content:           content,
		MessageType:       messageType,
	}, nil
}

func (s pipelineTestStore) SaveChannelMessageMentions(ctx context.Context, channelMessageID uuid.UUID, platform string, platformTenantID string, mentions []unifiedmessage.Mention) error {
	_ = ctx
	_ = channelMessageID
	_ = platform
	_ = platformTenantID
	_ = mentions
	return nil
}

type recordingRealtimeService struct {
	called bool
}

func (s *recordingRealtimeService) Handle(ctx context.Context, messageCtx realtime.MessageContext) {
	_ = ctx
	_ = messageCtx
	s.called = true
}

func TestProcessSkipsRealtimeForCommandLikeNonMemberMention(t *testing.T) {
	realtimeSvc := &recordingRealtimeService{}
	handler := &Handler{
		PlatformLabel:        "test",
		Persistence:          messagepersist.NewService(pipelineTestStore{}, messagepersist.NoopSenderNameResolver{}),
		NonCommandDispatcher: realtime.NewDispatcher(realtimeSvc),
	}

	saved := handler.Process(Input{
		Message: &unifiedmessage.Message{
			Platform:          "line",
			ChannelID:         "group-1",
			ChannelType:       "group",
			SenderID:          "user-1",
			PlatformMessageID: "msg-1",
			MessageType:       "text",
			Text:              "@Jarvis 幫我總結上面事情",
			Mentions:          []unifiedmessage.Mention{{UserID: "BOT001"}},
		},
		BotUserID:      "BOT001",
		PlatformUserID: "user-1",
	})

	if saved == nil {
		t.Fatal("expected message to be persisted")
	}
	if realtimeSvc.called {
		t.Fatal("expected command-like non-member mention to skip realtime dispatcher")
	}
}

func TestProcessDispatchesRealtimeForPlainNonCommandMessage(t *testing.T) {
	senderUserID := uuid.New()
	realtimeSvc := &recordingRealtimeService{}
	handler := &Handler{
		PlatformLabel:        "test",
		Persistence:          messagepersist.NewService(pipelineTestStore{senderUserID: &senderUserID}, messagepersist.NoopSenderNameResolver{}),
		NonCommandDispatcher: realtime.NewDispatcher(realtimeSvc),
	}

	saved := handler.Process(Input{
		Message: &unifiedmessage.Message{
			Platform:          "line",
			ChannelID:         "group-1",
			ChannelType:       "group",
			SenderID:          "user-1",
			PlatformMessageID: "msg-2",
			MessageType:       "text",
			Text:              "明天記得買咖啡",
		},
		BotUserID:      "BOT001",
		PlatformUserID: "user-1",
	})

	if saved == nil {
		t.Fatal("expected message to be persisted")
	}
	if !realtimeSvc.called {
		t.Fatal("expected plain non-command message to dispatch realtime")
	}
}
