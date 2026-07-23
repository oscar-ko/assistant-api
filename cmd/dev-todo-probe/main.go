package main

// dev-todo-probe 是只讀的開發診斷工具，用來把 Todo Reminder 相關表一次輸出成 JSON。
// 它刻意不呼叫 GraphQL API，而是直接讀 DB：模擬情境失敗時，可以分辨是 webhook/analyzer 沒落庫、
// candidate 沒 promotion、assignee 沒 resolve，還是 sender/reply metadata 寫錯。

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"sort"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/ent/channel"
	"assistant-api/internal/ent/channelmessage"
	"assistant-api/internal/ent/channelservicemember"
	"assistant-api/internal/ent/skill"
	"assistant-api/internal/ent/slack"
	"assistant-api/internal/ent/slackworkspace"
	"assistant-api/internal/ent/todo"
	"assistant-api/internal/ent/todocandidate"
	"assistant-api/internal/ent/todocandidateassignee"
	"assistant-api/internal/ent/todocandidateevidencemessage"
	"assistant-api/internal/ent/todoevent"
	"assistant-api/internal/ent/todoupdatecandidate"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	_ "github.com/lib/pq"
)

type probeChannel struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	GroupID               string `json:"group_id"`
	RealtimeTextScanReady bool   `json:"realtime_text_scan_ready"`
}

type probeSlackUser struct {
	UserID         string `json:"user_id"`
	PlatformTeamID string `json:"platform_team_id"`
	PlatformUserID string `json:"platform_user_id"`
	DisplayName    string `json:"display_name"`
}

type probeSlackWorkspace struct {
	AppID          string `json:"app_id"`
	PlatformTeamID string `json:"platform_team_id"`
	TeamName       string `json:"team_name"`
	BotUserID      string `json:"bot_user_id"`
}

type probeOutput struct {
	Channels        []probeChannel        `json:"channels"`
	SlackUsers      []probeSlackUser      `json:"slack_users"`
	SlackWorkspaces []probeSlackWorkspace `json:"slack_workspaces"`
	TodoTables      *probeTodoTables      `json:"todo_tables,omitempty"`
}

type probeTodoTables struct {
	ChannelID                     string                 `json:"channel_id"`
	ChannelMessageCount           int                    `json:"channel_message_count"`
	TodoCandidateCount            int                    `json:"todo_candidate_count"`
	TodoCandidateEvidenceCount    int                    `json:"todo_candidate_evidence_message_count"`
	TodoCandidateAssigneeCount    int                    `json:"todo_candidate_assignee_count"`
	TodoCount                     int                    `json:"todo_count"`
	TodoEventCount                int                    `json:"todo_event_count"`
	TodoUpdateCandidateCount      int                    `json:"todo_update_candidate_count"`
	SenderCounts                  []probeSenderCount     `json:"sender_counts"`
	LatestMessages                []probeChannelMessage  `json:"latest_messages"`
	ReplyMessages                 []probeChannelMessage  `json:"reply_messages"`
	TodoCandidates                []probeTodoCandidate   `json:"todo_candidates"`
	Todos                         []probeTodo            `json:"todos"`
	TodoEvents                    []probeTodoEvent       `json:"todo_events"`
	TodoUpdateCandidates          []probeTodoUpdate      `json:"todo_update_candidates"`
	TodoCandidateEvidenceMessages []probeCandidateAnchor `json:"todo_candidate_evidence_messages"`
	TodoCandidateAssignees        []probeCandidatePerson `json:"todo_candidate_assignees"`
}

type probeSenderCount struct {
	// SenderID/SenderUserID 直接反映 channel_messages 的落庫狀態；
	// BotAppID/BotName 則是 probe 額外用 slack_workspaces.bot_user_id 反查出的診斷資訊。
	// 這讓我們能同時確認「發話者是 bot」與「沒有被誤綁成 Oscar 這類系統使用者」。
	SenderID     string `json:"sender_id"`
	SenderUserID string `json:"sender_user_id"`
	BotAppID     string `json:"bot_app_id"`
	BotName      string `json:"bot_name"`
	Count        int    `json:"count"`
}

type probeChannelMessage struct {
	ID         string `json:"id"`
	SenderID   string `json:"sender_id"`
	SenderName string `json:"sender_name"`
	// sender_user_id 為空代表這則訊息只有平台身份，尚未綁定系統 User；
	// 多 bot visible simulation 會用這個欄位確認「外部長官 bot」沒有被誤寫成 Oscar。
	SenderUserID      string `json:"sender_user_id"`
	PlatformMessageID string `json:"platform_message_id"`
	ReplyToMsgID      string `json:"reply_to_msg_id"`
	Content           string `json:"content"`
}

type probeTodoCandidate struct {
	ID           string   `json:"id"`
	Status       string   `json:"status"`
	LastDecision string   `json:"last_decision"`
	Summary      string   `json:"summary"`
	Assignees    []string `json:"assignees"`
	DueText      string   `json:"due_text"`
	DueAt        string   `json:"due_at"`
	Missing      []string `json:"missing_fields"`
	Confidence   float64  `json:"confidence"`
	Reason       string   `json:"reason"`
}

type probeTodo struct {
	ID                string `json:"id"`
	OwnerUserID       string `json:"owner_user_id"`
	SourceCandidateID string `json:"source_candidate_id"`
	Status            string `json:"status"`
	Title             string `json:"title"`
	DueAt             string `json:"due_at"`
}

type probeTodoEvent struct {
	ID                string `json:"id"`
	TodoID            string `json:"todo_id"`
	SourceCandidateID string `json:"source_candidate_id"`
	SourceMessageID   string `json:"source_message_id"`
	EventType         string `json:"event_type"`
	Reason            string `json:"reason"`
}

type probeTodoUpdate struct {
	ID                string `json:"id"`
	TodoID            string `json:"todo_id"`
	SourceCandidateID string `json:"source_candidate_id"`
	SourceMessageID   string `json:"source_message_id"`
	ChangeType        string `json:"change_type"`
	Status            string `json:"status"`
	Reason            string `json:"reason"`
}

type probeCandidateAnchor struct {
	ID           string  `json:"id"`
	CandidateID  string  `json:"candidate_id"`
	MessageID    string  `json:"message_id"`
	RelationType string  `json:"relation_type"`
	Source       string  `json:"source"`
	Confidence   float64 `json:"confidence"`
	IsActive     bool    `json:"is_active"`
	Reason       string  `json:"reason"`
}

type probeCandidatePerson struct {
	ID               string `json:"id"`
	CandidateID      string `json:"candidate_id"`
	ResolvedUserID   string `json:"resolved_user_id"`
	Source           string `json:"source"`
	AssigneeText     string `json:"assignee_text"`
	ResolutionStatus string `json:"resolution_status"`
	SourceMentionID  string `json:"source_message_mention_id"`
	Reason           string `json:"reason"`
}

func main() {
	channelIDText := flag.String("channel", "", "channel id for todo table detail")
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

	channels, err := client.Channel.Query().Where(channel.PlatformEQ(channel.PlatformSlack), channel.TypeEQ(channel.TypeGroup)).Order(ent.Asc(channel.FieldCreatedAt)).All(ctx)
	if err != nil {
		log.Fatal(err)
	}
	output := probeOutput{Channels: make([]probeChannel, 0, len(channels))}
	for _, item := range channels {
		ready, err := client.ChannelServiceMember.Query().Where(
			channelservicemember.ChannelIDEQ(item.ID),
			channelservicemember.HasSkillWith(skill.IsRealtimeEQ(true), skill.RequiresTextScanEQ(true)),
		).Exist(ctx)
		if err != nil {
			log.Fatal(err)
		}
		output.Channels = append(output.Channels, probeChannel{ID: item.ID.String(), Name: item.Name, GroupID: item.GroupID, RealtimeTextScanReady: ready})
	}

	slackUsers, err := client.Slack.Query().WithUser().Order(ent.Asc(slack.FieldPlatformTeamID), ent.Asc(slack.FieldPlatformUserID)).All(ctx)
	if err != nil {
		log.Fatal(err)
	}
	output.SlackUsers = make([]probeSlackUser, 0, len(slackUsers))
	for _, item := range slackUsers {
		name := ""
		if item.DisplayName != nil {
			name = *item.DisplayName
		}
		output.SlackUsers = append(output.SlackUsers, probeSlackUser{UserID: item.UserID.String(), PlatformTeamID: item.PlatformTeamID, PlatformUserID: item.PlatformUserID, DisplayName: name})
	}

	workspaces, err := client.SlackWorkspace.Query().Order(ent.Asc(slackworkspace.FieldPlatformTeamID), ent.Asc(slackworkspace.FieldAppID)).All(ctx)
	if err != nil {
		log.Fatal(err)
	}
	output.SlackWorkspaces = make([]probeSlackWorkspace, 0, len(workspaces))
	for _, item := range workspaces {
		teamName := ""
		if item.TeamName != nil {
			teamName = *item.TeamName
		}
		botUserID := ""
		if item.BotUserID != nil {
			botUserID = *item.BotUserID
		}
		output.SlackWorkspaces = append(output.SlackWorkspaces, probeSlackWorkspace{AppID: item.AppID, PlatformTeamID: item.PlatformTeamID, TeamName: teamName, BotUserID: botUserID})
	}
	if *channelIDText != "" {
		channelID, err := uuid.Parse(*channelIDText)
		if err != nil {
			log.Fatal(err)
		}
		tables, err := probeTodoTableState(ctx, client, channelID)
		if err != nil {
			log.Fatal(err)
		}
		output.TodoTables = tables
	}

	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(data))
}

func probeTodoTableState(ctx context.Context, client *ent.Client, channelID uuid.UUID) (*probeTodoTables, error) {
	messageCount, err := client.ChannelMessage.Query().Where(channelmessage.ChannelIDEQ(channelID)).Count(ctx)
	if err != nil {
		return nil, err
	}
	candidateCount, err := client.TodoCandidate.Query().Where(todocandidate.ChannelIDEQ(channelID)).Count(ctx)
	if err != nil {
		return nil, err
	}
	evidenceCount, err := client.TodoCandidateEvidenceMessage.Query().Where(todocandidateevidencemessage.ChannelIDEQ(channelID)).Count(ctx)
	if err != nil {
		return nil, err
	}
	assigneeCount, err := client.TodoCandidateAssignee.Query().Where(todocandidateassignee.HasCandidateWith(todocandidate.ChannelIDEQ(channelID))).Count(ctx)
	if err != nil {
		return nil, err
	}
	todoCount, err := client.Todo.Query().Where(todo.ChannelIDEQ(channelID)).Count(ctx)
	if err != nil {
		return nil, err
	}
	todoEventCount, err := client.TodoEvent.Query().Where(todoevent.Or(
		todoevent.HasTodoWith(todo.ChannelIDEQ(channelID)),
		todoevent.HasSourceCandidateWith(todocandidate.ChannelIDEQ(channelID)),
		todoevent.HasSourceMessageWith(channelmessage.ChannelIDEQ(channelID)),
	)).Count(ctx)
	if err != nil {
		return nil, err
	}
	updateCount, err := client.TodoUpdateCandidate.Query().Where(todoupdatecandidate.Or(
		todoupdatecandidate.HasTodoWith(todo.ChannelIDEQ(channelID)),
		todoupdatecandidate.HasSourceCandidateWith(todocandidate.ChannelIDEQ(channelID)),
		todoupdatecandidate.HasSourceMessageWith(channelmessage.ChannelIDEQ(channelID)),
	)).Count(ctx)
	if err != nil {
		return nil, err
	}

	tables := &probeTodoTables{
		ChannelID:                     channelID.String(),
		ChannelMessageCount:           messageCount,
		TodoCandidateCount:            candidateCount,
		TodoCandidateEvidenceCount:    evidenceCount,
		TodoCandidateAssigneeCount:    assigneeCount,
		TodoCount:                     todoCount,
		TodoEventCount:                todoEventCount,
		TodoUpdateCandidateCount:      updateCount,
		SenderCounts:                  []probeSenderCount{},
		LatestMessages:                []probeChannelMessage{},
		ReplyMessages:                 []probeChannelMessage{},
		TodoCandidates:                []probeTodoCandidate{},
		Todos:                         []probeTodo{},
		TodoEvents:                    []probeTodoEvent{},
		TodoUpdateCandidates:          []probeTodoUpdate{},
		TodoCandidateEvidenceMessages: []probeCandidateAnchor{},
		TodoCandidateAssignees:        []probeCandidatePerson{},
	}

	botSenders, err := probeSlackBotSenderNames(ctx, client)
	if err != nil {
		return nil, err
	}

	allMessages, err := client.ChannelMessage.Query().Where(channelmessage.ChannelIDEQ(channelID)).All(ctx)
	if err != nil {
		return nil, err
	}
	senderCounts := make(map[string]probeSenderCount)
	for _, item := range allMessages {
		// sender_user_id 為空時，sender_id 仍可能是已安裝 Slack App 的 bot_user_id；
		// probe 在這裡把 bot_user_id 補成 app/name，避免只看 U0... 看不出是 Jarvis、Thor 還是 Hulk。
		senderUserID := ""
		if item.SenderUserID != nil {
			senderUserID = item.SenderUserID.String()
		}
		key := item.SenderID + "\x00" + senderUserID
		count := senderCounts[key]
		count.SenderID = item.SenderID
		count.SenderUserID = senderUserID
		if senderUserID == "" {
			if botSender, ok := botSenders[item.SenderID]; ok {
				count.BotAppID = botSender.AppID
				count.BotName = botSender.Name
			}
		}
		count.Count++
		senderCounts[key] = count
	}
	keys := make([]string, 0, len(senderCounts))
	for key := range senderCounts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		tables.SenderCounts = append(tables.SenderCounts, senderCounts[key])
	}

	messages, err := client.ChannelMessage.Query().Where(channelmessage.ChannelIDEQ(channelID)).Order(ent.Desc(channelmessage.FieldCreatedAt)).Limit(12).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range messages {
		senderUserID := ""
		if item.SenderUserID != nil {
			senderUserID = item.SenderUserID.String()
		}
		tables.LatestMessages = append(tables.LatestMessages, probeChannelMessage{ID: item.ID.String(), SenderID: item.SenderID, SenderName: item.SenderName, SenderUserID: senderUserID, PlatformMessageID: item.PlatformMessageID, ReplyToMsgID: item.ReplyToMsgID, Content: item.Content})
	}

	replyMessages, err := client.ChannelMessage.Query().Where(channelmessage.ChannelIDEQ(channelID), channelmessage.ReplyToMsgIDNEQ("")).Order(ent.Asc(channelmessage.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range replyMessages {
		senderUserID := ""
		if item.SenderUserID != nil {
			senderUserID = item.SenderUserID.String()
		}
		tables.ReplyMessages = append(tables.ReplyMessages, probeChannelMessage{ID: item.ID.String(), SenderID: item.SenderID, SenderName: item.SenderName, SenderUserID: senderUserID, PlatformMessageID: item.PlatformMessageID, ReplyToMsgID: item.ReplyToMsgID, Content: item.Content})
	}

	candidates, err := client.TodoCandidate.Query().Where(todocandidate.ChannelIDEQ(channelID)).Order(ent.Asc(todocandidate.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range candidates {
		dueAt := ""
		if item.DueAt != nil {
			dueAt = item.DueAt.Format("2006-01-02T15:04:05Z07:00")
		}
		tables.TodoCandidates = append(tables.TodoCandidates, probeTodoCandidate{ID: item.ID.String(), Status: string(item.Status), LastDecision: string(item.LastDecision), Summary: item.Summary, Assignees: item.Assignees, DueText: item.DueText, DueAt: dueAt, Missing: item.MissingFields, Confidence: item.Confidence, Reason: item.Reason})
	}

	todos, err := client.Todo.Query().Where(todo.ChannelIDEQ(channelID)).Order(ent.Asc(todo.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range todos {
		dueAt := ""
		if item.DueAt != nil {
			dueAt = item.DueAt.Format("2006-01-02T15:04:05Z07:00")
		}
		sourceCandidateID := ""
		if item.SourceCandidateID != nil {
			sourceCandidateID = item.SourceCandidateID.String()
		}
		tables.Todos = append(tables.Todos, probeTodo{ID: item.ID.String(), OwnerUserID: item.OwnerUserID.String(), SourceCandidateID: sourceCandidateID, Status: string(item.Status), Title: item.Title, DueAt: dueAt})
	}

	events, err := client.TodoEvent.Query().Where(todoevent.Or(
		todoevent.HasTodoWith(todo.ChannelIDEQ(channelID)),
		todoevent.HasSourceCandidateWith(todocandidate.ChannelIDEQ(channelID)),
		todoevent.HasSourceMessageWith(channelmessage.ChannelIDEQ(channelID)),
	)).Order(ent.Asc(todoevent.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range events {
		sourceCandidateID := ""
		if item.SourceCandidateID != nil {
			sourceCandidateID = item.SourceCandidateID.String()
		}
		sourceMessageID := ""
		if item.SourceMessageID != nil {
			sourceMessageID = item.SourceMessageID.String()
		}
		tables.TodoEvents = append(tables.TodoEvents, probeTodoEvent{ID: item.ID.String(), TodoID: item.TodoID.String(), SourceCandidateID: sourceCandidateID, SourceMessageID: sourceMessageID, EventType: string(item.EventType), Reason: item.Reason})
	}

	updates, err := client.TodoUpdateCandidate.Query().Where(todoupdatecandidate.Or(
		todoupdatecandidate.HasTodoWith(todo.ChannelIDEQ(channelID)),
		todoupdatecandidate.HasSourceCandidateWith(todocandidate.ChannelIDEQ(channelID)),
		todoupdatecandidate.HasSourceMessageWith(channelmessage.ChannelIDEQ(channelID)),
	)).Order(ent.Asc(todoupdatecandidate.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range updates {
		sourceMessageID := ""
		if item.SourceMessageID != nil {
			sourceMessageID = item.SourceMessageID.String()
		}
		tables.TodoUpdateCandidates = append(tables.TodoUpdateCandidates, probeTodoUpdate{ID: item.ID.String(), TodoID: item.TodoID.String(), SourceCandidateID: item.SourceCandidateID.String(), SourceMessageID: sourceMessageID, ChangeType: string(item.ChangeType), Status: string(item.Status), Reason: item.Reason})
	}

	evidence, err := client.TodoCandidateEvidenceMessage.Query().Where(todocandidateevidencemessage.ChannelIDEQ(channelID)).Order(ent.Asc(todocandidateevidencemessage.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range evidence {
		tables.TodoCandidateEvidenceMessages = append(tables.TodoCandidateEvidenceMessages, probeCandidateAnchor{ID: item.ID.String(), CandidateID: item.CandidateID.String(), MessageID: item.MessageID.String(), RelationType: string(item.RelationType), Source: string(item.Source), Confidence: item.Confidence, IsActive: item.IsActive, Reason: item.Reason})
	}

	assignees, err := client.TodoCandidateAssignee.Query().Where(todocandidateassignee.HasCandidateWith(todocandidate.ChannelIDEQ(channelID))).Order(ent.Asc(todocandidateassignee.FieldCreatedAt)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range assignees {
		resolvedUserID := ""
		if item.ResolvedUserID != nil {
			resolvedUserID = item.ResolvedUserID.String()
		}
		resolutionStatus := ""
		if item.ResolutionStatus != nil {
			resolutionStatus = string(*item.ResolutionStatus)
		}
		sourceMentionID := ""
		if item.SourceMessageMentionID != nil {
			sourceMentionID = item.SourceMessageMentionID.String()
		}
		tables.TodoCandidateAssignees = append(tables.TodoCandidateAssignees, probeCandidatePerson{ID: item.ID.String(), CandidateID: item.CandidateID.String(), ResolvedUserID: resolvedUserID, Source: string(item.Source), AssigneeText: item.AssigneeText, ResolutionStatus: resolutionStatus, SourceMentionID: sourceMentionID, Reason: item.Reason})
	}
	return tables, nil
}

type probeSlackBotSender struct {
	AppID string
	Name  string
}

func probeSlackBotSenderNames(ctx context.Context, client *ent.Client) (map[string]probeSlackBotSender, error) {
	// Slack bot user id 不存在 app.yml，而是 workspace install 後寫在 slack_workspaces；
	// bot 顯示名則來自 config.Slack.Bots。這裡刻意把兩者接起來，專門服務 DB probe 的可讀性。
	workspaces, err := client.SlackWorkspace.Query().All(ctx)
	if err != nil {
		return nil, err
	}
	items := make(map[string]probeSlackBotSender, len(workspaces))
	for _, workspace := range workspaces {
		if workspace == nil || workspace.BotUserID == nil {
			continue
		}
		botUserID := *workspace.BotUserID
		if botUserID == "" {
			continue
		}
		name := ""
		bot, err := config.Slack.BotByAppID(workspace.AppID)
		if err == nil {
			name = bot.Name
		}
		items[botUserID] = probeSlackBotSender{AppID: workspace.AppID, Name: name}
	}
	return items, nil
}
