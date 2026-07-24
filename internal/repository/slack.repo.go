package repository

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"assistant-api/internal/ent"
	"assistant-api/internal/ent/slack"
	"assistant-api/internal/ent/slackworkspace"
	"assistant-api/internal/ent/user"

	"github.com/google/uuid"
)

// SlackRepo 封裝 Slack 綁定流程使用到的資料存取。
//
// 這層只負責資料讀寫，不承擔 OAuth 流程判斷與錯誤語意轉換；
// 上層 bind service 會依回傳結果決定綁定策略。
type SlackRepo struct {
	db *ent.Client
}

var ErrSlackWorkspaceInstallNotFound = errors.New("slack workspace install not found")

func NewSlackRepo(db *ent.Client) *SlackRepo {
	return &SlackRepo{db: db}
}

// GetUserBySlackIdentity 依 platform_team_id + platform_user_id 查詢已綁定使用者。
//
// 為何要 platform_team_id + platform_user_id：
// - slack_user_id 只在單一 workspace 內唯一。
// - 加上 team_id 才能跨 workspace 正確識別同名或同 user id 的差異。
func (r *SlackRepo) GetUserBySlackIdentity(ctx context.Context, teamID string, slackUserID string) (*ent.User, error) {
	return r.db.User.Query().Where(user.HasSlackWith(slack.PlatformTeamIDEQ(teamID), slack.PlatformUserIDEQ(slackUserID))).Only(ctx)
}

// GetUserByEmail 依 email 查詢使用者。
func (r *SlackRepo) GetUserByEmail(ctx context.Context, email string) (*ent.User, error) {
	return r.db.User.Query().Where(user.EmailEQ(email)).Only(ctx)
}

// HasSlackBindingForUser 檢查使用者是否已綁定任一 Slack 帳號。
//
// 用於防止同一個系統 user 重複綁定到不同 Slack 身份，
// 避免後續通知路由與權限判斷出現歧義。
func (r *SlackRepo) HasSlackBindingForUser(ctx context.Context, userID uuid.UUID) (bool, error) {
	return r.db.Slack.Query().Where(slack.HasUserWith(user.IDEQ(userID))).Exist(ctx)
}

// CreateSlackBinding 建立 Slack 綁定資料。
//
// 寫入策略：
// - platform_team_id / platform_user_id / display_name 為主要身份資訊
// - email / picture 採可空欄位，交由上層 nullable 處理
// - user edge 為必填，確保綁定記錄一定能回溯到系統 user
func (r *SlackRepo) CreateSlackBinding(ctx context.Context, u *ent.User, teamID string, slackUserID string, displayName string, email *string, picture *string) error {
	_, err := r.db.Slack.Create().
		SetPlatformTeamID(teamID).
		SetPlatformUserID(slackUserID).
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

// UpsertWorkspaceInstall stores the bot installation credentials for a Slack app in a workspace.
//
// Slack 同一個 workspace 可能安裝多個 app，本系統也可能同時跑多個 Slack bot。
// 因此 workspace install 的唯一語意必須是 app_id + platform_team_id，不能只用 team_id。
// 否則 A bot 驗證成功後可能拿到 B bot token 或 bot_user_id，造成錯誤回覆與權限混線。
func (r *SlackRepo) UpsertWorkspaceInstall(ctx context.Context, appID string, teamID string, teamName string, botToken string, botUserID string) error {
	if r == nil || r.db == nil {
		return fmt.Errorf("slack repository not initialized")
	}
	appID = strings.TrimSpace(appID)
	teamID = strings.TrimSpace(teamID)
	teamName = strings.TrimSpace(teamName)
	botToken = strings.TrimSpace(botToken)
	botUserID = strings.TrimSpace(botUserID)
	if appID == "" {
		return fmt.Errorf("slack app id is required")
	}
	if teamID == "" {
		return fmt.Errorf("slack team id is required")
	}
	if botToken == "" {
		return fmt.Errorf("slack bot token is required")
	}

	existing, err := r.db.SlackWorkspace.Query().
		Where(slackworkspace.AppIDEQ(appID), slackworkspace.PlatformTeamIDEQ(teamID)).
		Only(ctx)
	if err == nil {
		update := r.db.SlackWorkspace.UpdateOneID(existing.ID).
			SetBotToken(botToken)
		if teamName != "" {
			update = update.SetTeamName(teamName)
		} else {
			update = update.ClearTeamName()
		}
		if botUserID != "" {
			update = update.SetBotUserID(botUserID)
		} else {
			update = update.ClearBotUserID()
		}
		_, err = update.Save(ctx)
		return err
	}
	if !ent.IsNotFound(err) {
		return err
	}

	create := r.db.SlackWorkspace.Create().
		SetAppID(appID).
		SetPlatformTeamID(teamID).
		SetBotToken(botToken)
	if teamName != "" {
		create = create.SetTeamName(teamName)
	}
	if botUserID != "" {
		create = create.SetBotUserID(botUserID)
	}
	_, err = create.Save(ctx)
	return err
}

// ResolveWorkspaceBotToken returns the installed bot token for the Slack app in a workspace.
//
// 呼叫端必須帶入 request-scoped appID；不要從全域目前 bot 推導，
// 因為 webhook service 是 singleton，多個 Slack app 的請求可能交錯進來。
func (r *SlackRepo) ResolveWorkspaceBotToken(ctx context.Context, appID string, teamID string) (string, error) {
	if r == nil || r.db == nil {
		return "", fmt.Errorf("slack repository not initialized")
	}
	appID = strings.TrimSpace(appID)
	teamID = strings.TrimSpace(teamID)
	if appID == "" {
		return "", fmt.Errorf("slack app id is empty")
	}
	if teamID == "" {
		return "", fmt.Errorf("slack team id is empty")
	}

	item, err := r.db.SlackWorkspace.Query().
		Where(slackworkspace.AppIDEQ(appID), slackworkspace.PlatformTeamIDEQ(teamID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", fmt.Errorf("%w for app %s team %s", ErrSlackWorkspaceInstallNotFound, appID, teamID)
		}
		return "", err
	}
	token := strings.TrimSpace(item.BotToken)
	if token == "" {
		return "", fmt.Errorf("slack workspace bot token is empty for app %s team %s", appID, teamID)
	}
	return token, nil
}

// ResolveWorkspaceBotUserID returns the installed bot user id for the Slack app in a workspace.
func (r *SlackRepo) ResolveWorkspaceBotUserID(ctx context.Context, appID string, teamID string) (string, error) {
	if r == nil || r.db == nil {
		return "", fmt.Errorf("slack repository not initialized")
	}
	appID = strings.TrimSpace(appID)
	teamID = strings.TrimSpace(teamID)
	if appID == "" {
		return "", fmt.Errorf("slack app id is empty")
	}
	if teamID == "" {
		return "", fmt.Errorf("slack team id is empty")
	}

	item, err := r.db.SlackWorkspace.Query().
		Where(slackworkspace.AppIDEQ(appID), slackworkspace.PlatformTeamIDEQ(teamID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return "", fmt.Errorf("%w for app %s team %s", ErrSlackWorkspaceInstallNotFound, appID, teamID)
		}
		return "", err
	}
	if item.BotUserID == nil {
		return "", fmt.Errorf("slack workspace bot user id is empty for app %s team %s", appID, teamID)
	}
	botUserID := strings.TrimSpace(*item.BotUserID)
	if botUserID == "" {
		return "", fmt.Errorf("slack workspace bot user id is empty for app %s team %s", appID, teamID)
	}
	return botUserID, nil
}

// HasWorkspaceInstall reports whether a Slack workspace install record already exists.
func (r *SlackRepo) HasWorkspaceInstall(ctx context.Context, appID string, teamID string) (bool, error) {
	if r == nil || r.db == nil {
		return false, fmt.Errorf("slack repository not initialized")
	}
	appID = strings.TrimSpace(appID)
	teamID = strings.TrimSpace(teamID)
	if appID == "" {
		return false, fmt.Errorf("slack app id is empty")
	}
	if teamID == "" {
		return false, fmt.Errorf("slack team id is empty")
	}
	return r.db.SlackWorkspace.Query().
		Where(slackworkspace.AppIDEQ(appID), slackworkspace.PlatformTeamIDEQ(teamID)).
		Exist(ctx)
}
