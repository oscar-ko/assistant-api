package main

// dev-context-probe 是只讀的 Conversation Context 診斷工具。
// 它直接讀 DB，不呼叫 GraphQL 或 AI：
// - 只有 -channel 時，輸出該 channel 最近訊息，方便找 source message。
// - 同時提供 -source 與 -task 時，輸出正式 context query 會使用的 selected messages 與 prompt preview。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/ent/channelmessage"
	"assistant-api/internal/repository"
	conversationcontext "assistant-api/internal/usecase/conversation_context"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

type probeOutput struct {
	ChannelID     string              `json:"channel_id,omitempty"`
	SourceMessage string              `json:"source_message_id,omitempty"`
	Latest        []probeMessage      `json:"latest_messages,omitempty"`
	Preview       *probePreviewOutput `json:"preview,omitempty"`
}

type probeMessage struct {
	ID                string `json:"id"`
	SenderID          string `json:"sender_id"`
	SenderName        string `json:"sender_name"`
	SenderUserID      string `json:"sender_user_id"`
	PlatformMessageID string `json:"platform_message_id"`
	ReplyToMsgID      string `json:"reply_to_msg_id"`
	MessageType       string `json:"message_type"`
	Content           string `json:"content"`
}

type probePreviewOutput struct {
	Task                 string         `json:"task"`
	SelectedMessageCount int            `json:"selected_message_count"`
	RecentLimit          int            `json:"recent_limit"`
	SelectedLimit        int            `json:"selected_limit"`
	MaxContextChars      int            `json:"max_context_chars"`
	PromptPreview        string         `json:"prompt_preview"`
	Messages             []probeMessage `json:"messages"`
}

func main() {
	channelIDText := flag.String("channel", "", "channel id for listing latest messages")
	sourceIDText := flag.String("source", "", "source ChannelMessage id for context preview")
	task := flag.String("task", "", "user task for context preview")
	excludeSenderID := flag.String("exclude-sender", "", "sender id to exclude from context preview, usually the bot sender id")
	limit := flag.Int("limit", 20, "latest message limit when -channel is used")
	flag.Parse()

	config.MustLoad()
	drv, err := entsql.Open(dialect.Postgres, config.PostgreSQL.GetDSN())
	if err != nil {
		log.Fatal(err)
	}
	defer drv.Close()

	client := ent.NewClient(ent.Driver(drv))
	defer client.Close()
	ctx := context.Background()

	output := probeOutput{}
	if *channelIDText != "" {
		channelID, err := uuid.Parse(*channelIDText)
		if err != nil {
			log.Fatal(err)
		}
		output.ChannelID = channelID.String()
		latest, err := listLatestMessages(ctx, client, channelID, *limit)
		if err != nil {
			log.Fatal(err)
		}
		output.Latest = latest
	}

	if *sourceIDText != "" {
		sourceID, err := uuid.Parse(*sourceIDText)
		if err != nil {
			log.Fatal(err)
		}
		output.SourceMessage = sourceID.String()
		if *task == "" {
			log.Fatal("-task is required when -source is provided")
		}
		preview, err := previewContext(ctx, client, sourceID, *task, *excludeSenderID)
		if err != nil {
			log.Fatal(err)
		}
		output.Preview = preview
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(data))
}

func listLatestMessages(ctx context.Context, client *ent.Client, channelID uuid.UUID, limit int) ([]probeMessage, error) {
	if limit <= 0 {
		limit = 20
	}
	items, err := client.ChannelMessage.Query().
		Where(channelmessage.ChannelIDEQ(channelID)).
		Order(ent.Desc(channelmessage.FieldCreatedAt)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	messages := make([]probeMessage, 0, len(items))
	for _, item := range items {
		messages = append(messages, toProbeMessage(item))
	}
	return messages, nil
}

func previewContext(ctx context.Context, client *ent.Client, sourceID uuid.UUID, task string, excludeSenderID string) (*probePreviewOutput, error) {
	repo := repository.NewChannelMessageRepo(client)
	source, err := repo.GetMessageByID(ctx, sourceID)
	if err != nil {
		return nil, err
	}
	if source == nil {
		return nil, fmt.Errorf("source message not found: %s", sourceID.String())
	}
	// 診斷工具刻意只建立 preview 用 service，不注入 LLM。
	// 這樣可以在不消耗模型、不改變資料狀態的情況下，重現正式流程會餵給 AI 的訊息選取與 prompt。
	service := conversationcontext.New(repo, nil, conversationcontext.Config{
		RecentMessageLimit: config.AI.ConversationContext.RecentMessageLimit,
		MaxContextMessages: config.AI.ConversationContext.MaxContextMessages,
		MaxContextChars:    config.AI.ConversationContext.MaxContextChars,
		ExcludedSenderIDs:  []string{excludeSenderID},
	})
	preview, err := service.Preview(ctx, source, task)
	if err != nil {
		return nil, err
	}
	messages := make([]probeMessage, 0, len(preview.Messages))
	for _, item := range preview.Messages {
		messages = append(messages, probeMessage{
			ID:                item.ID.String(),
			SenderID:          item.SenderID,
			SenderName:        item.SenderName,
			PlatformMessageID: item.PlatformMessageID,
			MessageType:       "text",
			Content:           item.Text,
		})
	}
	return &probePreviewOutput{
		Task:                 preview.Task,
		SelectedMessageCount: len(messages),
		RecentLimit:          preview.RecentLimit,
		SelectedLimit:        preview.SelectedLimit,
		MaxContextChars:      preview.MaxContextChars,
		PromptPreview:        preview.PromptText,
		Messages:             messages,
	}, nil
}

func toProbeMessage(item *ent.ChannelMessage) probeMessage {
	if item == nil {
		return probeMessage{}
	}
	senderUserID := ""
	if item.SenderUserID != nil {
		senderUserID = item.SenderUserID.String()
	}
	return probeMessage{
		ID:                item.ID.String(),
		SenderID:          item.SenderID,
		SenderName:        item.SenderName,
		SenderUserID:      senderUserID,
		PlatformMessageID: item.PlatformMessageID,
		ReplyToMsgID:      item.ReplyToMsgID,
		MessageType:       item.MessageType,
		Content:           item.Content,
	}
}
