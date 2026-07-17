package runtimecontext

import "context"

type contextKey string

const (
	workspaceTeamIDKey contextKey = "workspace_team_id"
	botSenderIDKey     contextKey = "bot_sender_id"
)

func WithWorkspaceTeamID(ctx context.Context, teamID string) context.Context {
	if teamID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, workspaceTeamIDKey, teamID)
}

func WorkspaceTeamIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	teamID, _ := ctx.Value(workspaceTeamIDKey).(string)
	return teamID
}

func WithBotSenderID(ctx context.Context, botSenderID string) context.Context {
	if botSenderID == "" {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, botSenderIDKey, botSenderID)
}

func BotSenderIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	botSenderID, _ := ctx.Value(botSenderIDKey).(string)
	return botSenderID
}
