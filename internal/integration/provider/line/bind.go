package line

import (
	"context"
	"fmt"
	"strings"

	"assistant-api/internal/ent"
	"assistant-api/internal/ent/line"
	"assistant-api/internal/ent/user"
)

// bindUser 將 LINE 帳號綁定到現有使用者，或建立新使用者與綁定資料。
func bindUser(ctx context.Context, client *ent.Client, p *profile) (*ent.User, error) {
	lineID := strings.TrimSpace(p.UserID)
	email := strings.TrimSpace(p.Email)
	name := strings.TrimSpace(p.DisplayName)
	picture := strings.TrimSpace(p.Picture)
	if name == "" {
		name = "LINE User"
	}

	u, err := client.User.Query().Where(user.HasLineWith(line.LineUserIDEQ(lineID))).Only(ctx)
	if err == nil {
		return u, nil
	}
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}

	if email != "" {
		uByEmail, e := client.User.Query().Where(user.EmailEQ(email)).Only(ctx)
		if e == nil {
			hasLine, he := client.Line.Query().Where(line.HasUserWith(user.IDEQ(uByEmail.ID))).Exist(ctx)
			if he != nil {
				return nil, he
			}
			if hasLine {
				return nil, fmt.Errorf("user already bound to another line account")
			}

			if _, ce := client.Line.Create().
				SetLineUserID(lineID).
				SetDisplayName(name).
				SetNillableEmail(nullable(email)).
				SetNillablePicture(nullable(picture)).
				SetUser(uByEmail).
				Save(ctx); ce != nil {
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

	uNew, err := client.User.Create().SetName(name).SetEmail(email).Save(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := client.Line.Create().
		SetLineUserID(lineID).
		SetDisplayName(name).
		SetNillableEmail(nullable(strings.TrimSpace(p.Email))).
		SetNillablePicture(nullable(picture)).
		SetUser(uNew).
		Save(ctx); err != nil {
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
