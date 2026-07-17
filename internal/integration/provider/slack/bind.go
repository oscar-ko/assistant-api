package slack

import (
	"context"
	"fmt"
	"strings"

	"assistant-api/internal/ent"

	"github.com/google/uuid"
)

// slackBindRepository 定義 Slack 綁定流程所需的資料操作。
//
// 設計原則：
// - 綁定流程只依賴抽象介面，不直接耦合 Ent client。
// - 讓路由層可專注 OAuth 編排，資料一致性由 repository 層保證。
type slackBindRepository interface {
	GetUserBySlackIdentity(ctx context.Context, teamID string, slackUserID string) (*ent.User, error)
	GetUserByEmail(ctx context.Context, email string) (*ent.User, error)
	HasSlackBindingForUser(ctx context.Context, userID uuid.UUID) (bool, error)
	CreateSlackBinding(ctx context.Context, u *ent.User, teamID string, slackUserID string, displayName string, email *string, picture *string) error
	CreateUser(ctx context.Context, name, email string) (*ent.User, error)
}

// bindUser 將 Slack 帳號綁定到現有使用者，或建立新使用者與綁定資料。
//
// 決策順序：
// 1) 先以 teamID + slackUserID 查詢：若已綁定，直接回既有 user。
// 2) 若尚未綁定，改以 email 查詢：
//   - 找到同 email user：檢查是否已綁定其他 Slack，避免一人多綁衝突。
//   - 找不到同 email user：建立新 user，再建立 Slack 綁定。
//
// 嚴格模式：
// - teamID / slackUserID / displayName / email 任何缺值都直接報錯。
// - 不產生假 email、不補預設名稱，避免後續資料污染與追蹤困難。
func bindUser(ctx context.Context, repo slackBindRepository, p *profile) (*ent.User, error) {
	teamID := strings.TrimSpace(p.TeamID)
	slackUserID := strings.TrimSpace(p.UserID)
	if teamID == "" || slackUserID == "" {
		return nil, fmt.Errorf("slack team id and user id are required")
	}

	email := strings.TrimSpace(p.Email)
	name := strings.TrimSpace(p.DisplayName)
	picture := strings.TrimSpace(p.Picture)
	if name == "" {
		return nil, fmt.Errorf("slack display name is required")
	}
	if email == "" {
		return nil, fmt.Errorf("slack email is required")
	}

	// 第一層：若 team+user 已存在綁定，直接返回，保持流程冪等。
	u, err := repo.GetUserBySlackIdentity(ctx, teamID, slackUserID)
	if err == nil {
		return u, nil
	}
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}

	uByEmail, e := repo.GetUserByEmail(ctx, email)
	if e == nil {
		// 同一位系統 user 僅允許綁定一個 Slack 身份，避免身份覆蓋衝突。
		hasSlack, he := repo.HasSlackBindingForUser(ctx, uByEmail.ID)
		if he != nil {
			return nil, he
		}
		if hasSlack {
			return nil, fmt.Errorf("user already bound to another slack account")
		}

		if ce := repo.CreateSlackBinding(ctx, uByEmail, teamID, slackUserID, name, nullable(email), nullable(picture)); ce != nil {
			return nil, ce
		}
		return uByEmail, nil
	}
	if e != nil && !ent.IsNotFound(e) {
		return nil, e
	}

	uNew, err := repo.CreateUser(ctx, name, email)
	if err != nil {
		return nil, err
	}

	// 新 user 建立後立即寫入 Slack 綁定，確保回傳資料一致。
	if err := repo.CreateSlackBinding(ctx, uNew, teamID, slackUserID, name, nullable(email), nullable(picture)); err != nil {
		return nil, err
	}

	return uNew, nil
}

// nullable 將空字串轉為 nil，便於寫入可空欄位。
func nullable(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}
