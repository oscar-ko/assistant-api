package graph

import (
	"context"
	"fmt"

	"assistant-api/internal/ent/actionresult"
	"assistant-api/internal/ent/channelmessage"
	"assistant-api/internal/ent/channelmessagemention"
	"assistant-api/internal/ent/todo"
	"assistant-api/internal/ent/todocandidate"
	"assistant-api/internal/ent/todocandidateassignee"
	"assistant-api/internal/ent/todocandidateevidencemessage"
	"assistant-api/internal/ent/todoevent"
	"assistant-api/internal/ent/todoupdatecandidate"
	"assistant-api/internal/graph/model"

	"github.com/google/uuid"
)

type devRealtimeTodoClearCounts struct {
	TodoCount                         int
	TodoEventCount                    int
	TodoUpdateCandidateCount          int
	ChannelMessageCount               int
	TodoCandidateCount                int
	TodoCandidateEvidenceMessageCount int
	TodoCandidateAssigneeCount        int
	ChannelMessageMentionCount        int
	ActionResultCount                 int
}

func (c devRealtimeTodoClearCounts) toGraphQLPayload() *model.ClearDevRealtimeTodoDataPayload {
	return &model.ClearDevRealtimeTodoDataPayload{
		Status:                            "cleared",
		TodoCount:                         c.TodoCount,
		TodoEventCount:                    c.TodoEventCount,
		TodoUpdateCandidateCount:          c.TodoUpdateCandidateCount,
		ChannelMessageCount:               c.ChannelMessageCount,
		TodoCandidateCount:                c.TodoCandidateCount,
		TodoCandidateEvidenceMessageCount: c.TodoCandidateEvidenceMessageCount,
		TodoCandidateAssigneeCount:        c.TodoCandidateAssigneeCount,
		ChannelMessageMentionCount:        c.ChannelMessageMentionCount,
		ActionResultCount:                 c.ActionResultCount,
	}
}

func (r *Resolver) clearDevRealtimeTodoData(ctx context.Context, channelID uuid.UUID) (devRealtimeTodoClearCounts, error) {
	// 刪除順序必須順著外鍵依賴方向從葉節點往上游刪，否則會因子資料尚存在而卡在外鍵限制。
	actionResultCount, err := r.Client.ActionResult.Delete().Where(
		actionresult.HasChannelMessageWith(channelmessage.ChannelIDEQ(channelID)),
	).Exec(ctx)
	if err != nil {
		return devRealtimeTodoClearCounts{}, fmt.Errorf("delete action results failed: %w", err)
	}
	todoEventCount, err := r.Client.TodoEvent.Delete().Where(
		todoevent.Or(
			todoevent.HasTodoWith(todo.ChannelIDEQ(channelID)),
			todoevent.HasSourceCandidateWith(todocandidate.ChannelIDEQ(channelID)),
			todoevent.HasSourceMessageWith(channelmessage.ChannelIDEQ(channelID)),
		),
	).Exec(ctx)
	if err != nil {
		return devRealtimeTodoClearCounts{}, fmt.Errorf("delete todo events failed: %w", err)
	}
	todoUpdateCandidateCount, err := r.Client.TodoUpdateCandidate.Delete().Where(
		todoupdatecandidate.Or(
			todoupdatecandidate.HasTodoWith(todo.ChannelIDEQ(channelID)),
			todoupdatecandidate.HasSourceCandidateWith(todocandidate.ChannelIDEQ(channelID)),
			todoupdatecandidate.HasSourceMessageWith(channelmessage.ChannelIDEQ(channelID)),
		),
	).Exec(ctx)
	if err != nil {
		return devRealtimeTodoClearCounts{}, fmt.Errorf("delete todo update candidates failed: %w", err)
	}
	todoCount, err := r.Client.Todo.Delete().Where(
		todo.Or(
			todo.ChannelIDEQ(channelID),
			todo.HasSourceCandidateWith(todocandidate.ChannelIDEQ(channelID)),
		),
	).Exec(ctx)
	if err != nil {
		return devRealtimeTodoClearCounts{}, fmt.Errorf("delete todos failed: %w", err)
	}
	todoCandidateAssigneeCount, err := r.Client.TodoCandidateAssignee.Delete().Where(
		todocandidateassignee.Or(
			todocandidateassignee.HasCandidateWith(todocandidate.ChannelIDEQ(channelID)),
			todocandidateassignee.HasSourceMessageMentionWith(
				channelmessagemention.HasMessageWith(channelmessage.ChannelIDEQ(channelID)),
			),
		),
	).Exec(ctx)
	if err != nil {
		return devRealtimeTodoClearCounts{}, fmt.Errorf("delete todo candidate assignees failed: %w", err)
	}
	todoCandidateEvidenceMessageCount, err := r.Client.TodoCandidateEvidenceMessage.Delete().Where(
		todocandidateevidencemessage.ChannelIDEQ(channelID),
	).Exec(ctx)
	if err != nil {
		return devRealtimeTodoClearCounts{}, fmt.Errorf("delete todo candidate evidence messages failed: %w", err)
	}
	channelMessageMentionCount, err := r.Client.ChannelMessageMention.Delete().Where(
		channelmessagemention.HasMessageWith(channelmessage.ChannelIDEQ(channelID)),
	).Exec(ctx)
	if err != nil {
		return devRealtimeTodoClearCounts{}, fmt.Errorf("delete channel message mentions failed: %w", err)
	}
	todoCandidateCount, err := r.Client.TodoCandidate.Delete().Where(todocandidate.ChannelIDEQ(channelID)).Exec(ctx)
	if err != nil {
		return devRealtimeTodoClearCounts{}, fmt.Errorf("delete todo candidates failed: %w", err)
	}
	channelMessageCount, err := r.Client.ChannelMessage.Delete().Where(channelmessage.ChannelIDEQ(channelID)).Exec(ctx)
	if err != nil {
		return devRealtimeTodoClearCounts{}, fmt.Errorf("delete channel messages failed: %w", err)
	}

	return devRealtimeTodoClearCounts{
		TodoCount:                         todoCount,
		TodoEventCount:                    todoEventCount,
		TodoUpdateCandidateCount:          todoUpdateCandidateCount,
		ChannelMessageCount:               channelMessageCount,
		TodoCandidateCount:                todoCandidateCount,
		TodoCandidateEvidenceMessageCount: todoCandidateEvidenceMessageCount,
		TodoCandidateAssigneeCount:        todoCandidateAssigneeCount,
		ChannelMessageMentionCount:        channelMessageMentionCount,
		ActionResultCount:                 actionResultCount,
	}, nil
}
