package repository

import (
	"context"

	"assistant-api/internal/ent"
	"assistant-api/internal/ent/slack"
	"assistant-api/internal/ent/user"

	"github.com/google/uuid"
)

// SlackRepo 封裝 Slack 綁定流程使用到的資料存取。
type SlackRepo struct {
	db *ent.Client
}

func NewSlackRepo(db *ent.Client) *SlackRepo {
	return &SlackRepo{db: db}
}

// GetUserBySlackIdentity 依 team_id + slack_user_id 查詢已綁定使用者。
func (r *SlackRepo) GetUserBySlackIdentity(ctx context.Context, teamID string, slackUserID string) (*ent.User, error) {
	return r.db.User.Query().Where(user.HasSlackWith(slack.TeamIDEQ(teamID), slack.SlackUserIDEQ(slackUserID))).Only(ctx)
}

// GetUserByEmail 依 email 查詢使用者。
func (r *SlackRepo) GetUserByEmail(ctx context.Context, email string) (*ent.User, error) {
	return r.db.User.Query().Where(user.EmailEQ(email)).Only(ctx)
}

// HasSlackBindingForUser 檢查使用者是否已綁定任一 Slack 帳號。
func (r *SlackRepo) HasSlackBindingForUser(ctx context.Context, userID uuid.UUID) (bool, error) {
	return r.db.Slack.Query().Where(slack.HasUserWith(user.IDEQ(userID))).Exist(ctx)
}

// CreateSlackBinding 建立 Slack 綁定資料。
func (r *SlackRepo) CreateSlackBinding(ctx context.Context, u *ent.User, teamID string, slackUserID string, displayName string, email *string, picture *string) error {
	_, err := r.db.Slack.Create().
		SetTeamID(teamID).
		SetSlackUserID(slackUserID).
		SetDisplayName(displayName).
		SetNillableEmail(email).
		SetNillablePicture(picture).
		SetUser(u).
		Save(ctx)
	return err
}

// CreateUser 建立新使用者。
func (r *SlackRepo) CreateUser(ctx context.Context, name, email string) (*ent.User, error) {
	return r.db.User.Create().SetName(name).SetEmail(email).Save(ctx)
}
