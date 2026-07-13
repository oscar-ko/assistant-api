package line

import (
	"context"
	"fmt"
	"strings"

	"assistant-api/internal/ent"

	"github.com/google/uuid"
)

// lineBindRepository 定義綁定流程所需的資料操作。
type lineBindRepository interface {
	GetUserByLineUserID(ctx context.Context, lineUserID string) (*ent.User, error)
	GetUserByEmail(ctx context.Context, email string) (*ent.User, error)
	HasLineBindingForUser(ctx context.Context, userID uuid.UUID) (bool, error)
	CreateLineBinding(ctx context.Context, u *ent.User, lineUserID string, displayName string, picture *string) error
	CreateUser(ctx context.Context, name, email string) (*ent.User, error)
}

// bindUser 將 LINE 帳號綁定到現有使用者，或建立新使用者與綁定資料。
func bindUser(ctx context.Context, repo lineBindRepository, p *profile) (*ent.User, error) {
	lineID := strings.TrimSpace(p.UserID)
	email := strings.TrimSpace(p.Email)
	name := strings.TrimSpace(p.DisplayName)
	picture := strings.TrimSpace(p.Picture)
	if name == "" {
		name = "LINE User"
	}

	u, err := repo.GetUserByLineUserID(ctx, lineID)
	if err == nil {
		return u, nil
	}
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}

	if email != "" {
		uByEmail, e := repo.GetUserByEmail(ctx, email)
		if e == nil {
			hasLine, he := repo.HasLineBindingForUser(ctx, uByEmail.ID)
			if he != nil {
				return nil, he
			}
			if hasLine {
				return nil, fmt.Errorf("user already bound to another line account")
			}

			if ce := repo.CreateLineBinding(ctx, uByEmail, lineID, name, nullable(picture)); ce != nil {
				return nil, ce
			}
			return uByEmail, nil
		}
		if e != nil && !ent.IsNotFound(e) {
			return nil, e
		}
	} else {
		email = fmt.Sprintf("line_%s@line.local", lineID)
	}

	uNew, err := repo.CreateUser(ctx, name, email)
	if err != nil {
		return nil, err
	}

	if err := repo.CreateLineBinding(ctx, uNew, lineID, name, nullable(picture)); err != nil {
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
