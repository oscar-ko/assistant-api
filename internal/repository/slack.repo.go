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

// UpsertWorkspaceInstall stores the bot installation credentials for a Slack workspace.
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
		return fmt.Errorf("slack app_id is required")
	}
	if teamID == "" {
		return fmt.Errorf("slack team id is required")
	}
	if botToken == "" {
		return fmt.Errorf("slack bot token is required")
	}

	// workspace install 的唯一語意是「某個 Slack App 安裝到某個 workspace」。
	// 同一個 team 可以同時安裝多個 App，因此用 app_id + platform_team_id 作為查詢邊界，避免 token 覆蓋。
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

// ResolveWorkspaceBotToken returns the installed bot token for the Slack workspace.
func (r *SlackRepo) ResolveWorkspaceBotToken(ctx context.Context, appID string, teamID string) (string, error) {
	if r == nil || r.db == nil {
		return "", fmt.Errorf("slack repository not initialized")
	}
	appID = strings.TrimSpace(appID)
	teamID = strings.TrimSpace(teamID)
	if appID == "" {
		return "", fmt.Errorf("slack app_id is empty")
	}
	if teamID == "" {
		return "", fmt.Errorf("slack team id is empty")
	}

	// 出站訊息必須用 webhook/request context 帶下來的 app_id 與 team_id 查 token。
	// 這裡不回退到 default bot，否則多 bot 安裝時會把訊息送成錯誤的 Slack App。
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

// ResolveWorkspaceBotUserID returns the installed bot user id for the Slack workspace.
func (r *SlackRepo) ResolveWorkspaceBotUserID(ctx context.Context, appID string, teamID string) (string, error) {
	if r == nil || r.db == nil {
		return "", fmt.Errorf("slack repository not initialized")
	}
	appID = strings.TrimSpace(appID)
	teamID = strings.TrimSpace(teamID)
	if appID == "" {
		return "", fmt.Errorf("slack app_id is empty")
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
		return false, fmt.Errorf("slack app_id is empty")
	}
	if teamID == "" {
		return false, fmt.Errorf("slack team id is empty")
	}
	return r.db.SlackWorkspace.Query().
		Where(slackworkspace.AppIDEQ(appID), slackworkspace.PlatformTeamIDEQ(teamID)).
		Exist(ctx)
}
