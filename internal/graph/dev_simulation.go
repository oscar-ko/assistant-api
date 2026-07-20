package graph

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"github.com/google/uuid"
)

type devSimulationParticipant struct {
	UserID     uuid.UUID
	LineUserID string
	Name       string
}

type simulatedLineWebhookBody struct {
	Events []simulatedLineWebhookEvent `json:"events"`
}

type simulatedLineWebhookEvent struct {
	Type      string                      `json:"type"`
	Source    simulatedLineWebhookSource  `json:"source"`
	Message   simulatedLineWebhookMessage `json:"message"`
	Timestamp int64                       `json:"timestamp"`
}

type simulatedLineWebhookSource struct {
	Type    string `json:"type"`
	UserID  string `json:"userId"`
	GroupID string `json:"groupId"`
}

type simulatedLineWebhookMessage struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	Text string `json:"text"`
}

func (r *Resolver) pickRandomLineParticipants(ctx context.Context, count int) ([]devSimulationParticipant, error) {
	lines, err := r.Client.Line.Query().WithUser().All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query line users failed: %w", err)
	}
	if len(lines) < count {
		return nil, fmt.Errorf("not enough line-bound users: need %d, got %d", count, len(lines))
	}

	participants := make([]devSimulationParticipant, 0, len(lines))
	for _, item := range lines {
		if item == nil || strings.TrimSpace(item.PlatformUserID) == "" {
			continue
		}
		user, err := item.Edges.UserOrErr()
		if err != nil {
			return nil, fmt.Errorf("line user edge is not loaded: %w", err)
		}
		name := strings.TrimSpace(user.Name)
		if item.DisplayName != nil && strings.TrimSpace(*item.DisplayName) != "" {
			name = strings.TrimSpace(*item.DisplayName)
		}
		if name == "" {
			return nil, fmt.Errorf("line-bound user display name is empty: %s", user.ID.String())
		}
		participants = append(participants, devSimulationParticipant{UserID: user.ID, LineUserID: strings.TrimSpace(item.PlatformUserID), Name: name})
	}
	if len(participants) < count {
		return nil, fmt.Errorf("not enough usable line-bound users: need %d, got %d", count, len(participants))
	}
	if err := shuffleParticipants(participants); err != nil {
		return nil, err
	}
	return participants[:count], nil
}

func shuffleParticipants(items []devSimulationParticipant) error {
	for index := len(items) - 1; index > 0; index-- {
		randomIndex, err := cryptoRandomInt(index + 1)
		if err != nil {
			return fmt.Errorf("randomize participants failed: %w", err)
		}
		items[index], items[randomIndex] = items[randomIndex], items[index]
	}
	return nil
}

func cryptoRandomInt(limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("random limit must be positive")
	}
	value, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(limit)))
	if err != nil {
		return 0, err
	}
	return int(value.Int64()), nil
}

func buildCasualTodoSimulationText(index int, participants []devSimulationParticipant) string {
	names := make([]string, 0, len(participants))
	for _, participant := range participants {
		names = append(names, participant.Name)
	}
	assignee := names[index%len(names)]
	backup := names[(index+1)%len(names)]

	turns := []string{
		"欸明天下午三點前那個報價單誰要丟給小林啦",
		assignee + " 你幫我記一下好不好，我等下開完會會忘記",
		"可以啊但你們資料先補齊欸，少型號我沒辦法送",
		"型號在群組相簿那張，晚點七點前我傳新版給你",
		backup + " 如果我沒回你就直接打給我，拜託不要等到明天早上",
		"好啦我先把待辦記起來：明天下午三點前報價單給小林，負責人先抓 " + assignee,
		"還有會議室週五早上十點要改成大的那間，這個也順手記一下",
		"週五那個我處理，報價單不要又拖到下班才講喔",
	}
	return turns[index%len(turns)]
}
