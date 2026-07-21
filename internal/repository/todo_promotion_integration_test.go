//go:build cgo

package repository

import (
	"context"
	"testing"
	"time"

	"assistant-api/internal/ent/enttest"
	"assistant-api/internal/ent/todo"
	"assistant-api/internal/ent/todoevent"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

func TestSaveTodoCandidateFromAnalysisPromotesReadyCandidateToTodo(t *testing.T) {
	// 這個測試鎖住 Candidate -> Todo promotion 的完整資料邊界：
	// 1. 完整 candidate 會建立正式 Todo。
	// 2. promotion 會同步寫入 TodoEvent created audit trail。
	// 3. event 的 source 與 new_values 可回溯到原 candidate/message 與正式 Todo 欄位。
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:todo_promotion?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	repo := NewChannelMessageRepo(client)
	channel := client.Channel.Create().
		SetName("todo-test").
		SetPlatform("line").
		SetGroupID("group-1").
		SetType("group").
		SaveX(ctx)
	owner := client.User.Create().
		SetName("阿明").
		SetEmail("aming@example.com").
		SaveX(ctx)
	message := client.ChannelMessage.Create().
		SetChannelID(channel.ID).
		SetContent("阿明明天上午十點交報價單").
		SetSenderID("line-user-1").
		SetSenderName("阿明").
		SetSenderUserID(owner.ID).
		SetMessageType("text").
		SaveX(ctx)
	dueAt := time.Date(2026, 7, 22, 10, 0, 0, 0, time.FixedZone("Asia/Taipei", 8*60*60))

	candidate, err := repo.SaveTodoCandidateFromAnalysis(ctx, SaveTodoCandidateInput{
		ChannelID:     channel.ID,
		MessageID:     message.ID,
		Decision:      "create_candidate",
		Summary:       "交報價單",
		Assignees:     []string{"阿明"},
		DueText:       "明天上午十點",
		DueAt:         &dueAt,
		DueTimezone:   "Asia/Taipei",
		DuePrecision:  "datetime",
		DueDecision:   "normalized",
		DueConfidence: 0.93,
		Confidence:    0.91,
		Reason:        "message describes a complete todo",
	})
	if err != nil {
		t.Fatalf("expected ready candidate to persist and promote: %v", err)
	}
	if candidate == nil || candidate.ID == uuid.Nil {
		t.Fatalf("expected persisted candidate, got %+v", candidate)
	}

	promoted := client.Todo.Query().OnlyX(ctx)
	if promoted.SourceCandidateID == nil || *promoted.SourceCandidateID != candidate.ID {
		t.Fatalf("expected todo source candidate %s, got %+v", candidate.ID, promoted.SourceCandidateID)
	}
	if promoted.OwnerUserID != owner.ID || promoted.ChannelID != channel.ID {
		t.Fatalf("unexpected promoted todo identity: %+v", promoted)
	}
	if promoted.Status != todo.StatusActive || promoted.Title != "交報價單" {
		t.Fatalf("unexpected promoted todo content: %+v", promoted)
	}
	if promoted.DueAt == nil || !promoted.DueAt.Equal(dueAt) || promoted.DueTimezone != "Asia/Taipei" || promoted.DuePrecision != todo.DuePrecisionDatetime {
		t.Fatalf("unexpected promoted todo due fields: %+v", promoted)
	}
	event := client.TodoEvent.Query().OnlyX(ctx)
	if event.TodoID != promoted.ID || event.SourceCandidateID == nil || *event.SourceCandidateID != candidate.ID || event.SourceMessageID == nil || *event.SourceMessageID != message.ID {
		t.Fatalf("unexpected todo event identity: %+v", event)
	}
	if event.EventType != todoevent.EventTypeCreated || event.Confidence != 0.91 || event.Reason != "message describes a complete todo" {
		t.Fatalf("unexpected todo event metadata: %+v", event)
	}
	if len(event.OldValues) != 0 {
		t.Fatalf("expected created event old_values to be empty, got %+v", event.OldValues)
	}
	if event.NewValues["title"] != "交報價單" || event.NewValues["due_at"] != dueAt.Format(time.RFC3339) || event.NewValues["due_precision"] != "datetime" {
		t.Fatalf("unexpected todo event new_values: %+v", event.NewValues)
	}
}
