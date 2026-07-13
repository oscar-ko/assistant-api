package repository

import (
	"context"

	"assistant-api/internal/ent"
	"assistant-api/internal/ent/line"
	"assistant-api/internal/ent/user"

	"github.com/google/uuid"
)

// LineRepo 封裝 LINE 綁定流程使用到的資料存取。
type LineRepo struct {
	db *ent.Client
}

func NewLineRepo(db *ent.Client) *LineRepo {
	return &LineRepo{db: db}
}

// GetUserByLineUserID 依 LINE user id 查詢已綁定使用者。
func (r *LineRepo) GetUserByLineUserID(ctx context.Context, lineUserID string) (*ent.User, error) {
	return r.db.User.Query().Where(user.HasLineWith(line.LineUserIDEQ(lineUserID))).Only(ctx)
}

// GetUserByEmail 依 email 查詢使用者。
func (r *LineRepo) GetUserByEmail(ctx context.Context, email string) (*ent.User, error) {
	return r.db.User.Query().Where(user.EmailEQ(email)).Only(ctx)
}

// HasLineBindingForUser 檢查使用者是否已綁定任一 LINE 帳號。
func (r *LineRepo) HasLineBindingForUser(ctx context.Context, userID uuid.UUID) (bool, error) {
	return r.db.Line.Query().Where(line.HasUserWith(user.IDEQ(userID))).Exist(ctx)
}

// CreateLineBinding 建立 LINE 綁定資料。
func (r *LineRepo) CreateLineBinding(ctx context.Context, u *ent.User, lineUserID string, displayName string, email *string, picture *string) error {
	_, err := r.db.Line.Create().
		SetLineUserID(lineUserID).
		SetDisplayName(displayName).
		SetNillableEmail(email).
		SetNillablePicture(picture).
		SetUser(u).
		Save(ctx)
	return err
}

// CreateUser 建立新使用者。
func (r *LineRepo) CreateUser(ctx context.Context, name, email string) (*ent.User, error) {
	return r.db.User.Create().SetName(name).SetEmail(email).Save(ctx)
}
