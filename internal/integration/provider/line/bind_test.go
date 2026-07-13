package line

import (
	"context"
	"errors"
	"strings"
	"testing"

	"assistant-api/internal/ent"

	"github.com/google/uuid"
)

type mockLineBindRepo struct {
	// 下面這些 function 欄位是「可覆寫行為」：
	// 每個測試只需覆寫自己關心的方法，其餘走預設值，避免重複 setup。
	getUserByLineUserIDFn   func(ctx context.Context, lineUserID string) (*ent.User, error)
	getUserByEmailFn        func(ctx context.Context, email string) (*ent.User, error)
	hasLineBindingForUserFn func(ctx context.Context, userID uuid.UUID) (bool, error)
	createLineBindingFn     func(ctx context.Context, u *ent.User, lineUserID, displayName string, email, picture *string) error
	createUserFn            func(ctx context.Context, name, email string) (*ent.User, error)

	// 這些 counter 用來驗證「有沒有呼叫到」寫入操作。
	createLineBindingCalled int
	createUserCalled        int
}

func (m *mockLineBindRepo) GetUserByLineUserID(ctx context.Context, lineUserID string) (*ent.User, error) {
	if m.getUserByLineUserIDFn != nil {
		return m.getUserByLineUserIDFn(ctx, lineUserID)
	}
	// 預設回 NotFound，等同資料庫查無資料。
	// 這能讓測試自然走到 bindUser 的後續分支，除非案例有特別覆寫。
	return nil, &ent.NotFoundError{}
}

func (m *mockLineBindRepo) GetUserByEmail(ctx context.Context, email string) (*ent.User, error) {
	if m.getUserByEmailFn != nil {
		return m.getUserByEmailFn(ctx, email)
	}
	// 預設同樣回 NotFound，讓案例只聚焦自己要模擬的條件。
	return nil, &ent.NotFoundError{}
}

func (m *mockLineBindRepo) HasLineBindingForUser(ctx context.Context, userID uuid.UUID) (bool, error) {
	if m.hasLineBindingForUserFn != nil {
		return m.hasLineBindingForUserFn(ctx, userID)
	}
	// 預設視為尚未綁定。
	return false, nil
}

func (m *mockLineBindRepo) CreateLineBinding(ctx context.Context, u *ent.User, lineUserID, displayName string, email, picture *string) error {
	m.createLineBindingCalled++
	if m.createLineBindingFn != nil {
		return m.createLineBindingFn(ctx, u, lineUserID, displayName, email, picture)
	}
	// 預設成功；是否應被呼叫由 counter 驗證。
	return nil
}

func (m *mockLineBindRepo) CreateUser(ctx context.Context, name, email string) (*ent.User, error) {
	m.createUserCalled++
	if m.createUserFn != nil {
		return m.createUserFn(ctx, name, email)
	}
	// 預設回一個可用的 user，讓非重點案例不必重複建立假資料。
	return &ent.User{ID: uuid.New(), Name: name, Email: email}, nil
}

// 案例：line user id 已有綁定時，應直接回傳既有 user，且不做任何寫入。
func TestBindUser_ReturnExistingUserByLineID(t *testing.T) {
	repo := &mockLineBindRepo{
		getUserByLineUserIDFn: func(ctx context.Context, lineUserID string) (*ent.User, error) {
			existingID := uuid.New()
			return &ent.User{ID: existingID, Name: "Existing", Email: "existing@example.com"}, nil
		},
	}

	u, err := bindUser(context.Background(), repo, &profile{UserID: "U-1", DisplayName: "Name", Email: "a@b.com"})
	if err != nil {
		t.Fatalf("bindUser returned error: %v", err)
	}
	if u == nil {
		t.Fatalf("expected existing user ID 1, got %+v", u)
	}
	if repo.createLineBindingCalled != 0 || repo.createUserCalled != 0 {
		t.Fatalf("expected no create operations, got createLineBinding=%d createUser=%d", repo.createLineBindingCalled, repo.createUserCalled)
	}
}

// 案例：email 找到 user，但該 user 已綁其他 line，應回錯誤且不建立新綁定。
func TestBindUser_EmailExistsAlreadyBound_ReturnError(t *testing.T) {
	userID := uuid.New()
	repo := &mockLineBindRepo{
		getUserByLineUserIDFn: func(ctx context.Context, lineUserID string) (*ent.User, error) {
			return nil, &ent.NotFoundError{}
		},
		getUserByEmailFn: func(ctx context.Context, email string) (*ent.User, error) {
			return &ent.User{ID: userID, Name: "ByEmail", Email: email}, nil
		},
		hasLineBindingForUserFn: func(ctx context.Context, id uuid.UUID) (bool, error) {
			return true, nil
		},
	}

	_, err := bindUser(context.Background(), repo, &profile{UserID: "U-2", DisplayName: "Name", Email: "exists@example.com"})
	if err == nil {
		t.Fatal("expected error when user already bound to another line account")
	}
	if !strings.Contains(err.Error(), "already bound") {
		t.Fatalf("expected already bound error, got: %v", err)
	}
	if repo.createLineBindingCalled != 0 {
		t.Fatalf("expected no line binding creation, got %d", repo.createLineBindingCalled)
	}
}

// 案例：email 找到 user 且尚未綁 line，應建立 line 綁定且不新增 user。
func TestBindUser_EmailExistsWithoutLine_CreatesBinding(t *testing.T) {
	userID := uuid.New()
	repo := &mockLineBindRepo{
		getUserByLineUserIDFn: func(ctx context.Context, lineUserID string) (*ent.User, error) {
			return nil, &ent.NotFoundError{}
		},
		getUserByEmailFn: func(ctx context.Context, email string) (*ent.User, error) {
			return &ent.User{ID: userID, Name: "ByEmail", Email: email}, nil
		},
		hasLineBindingForUserFn: func(ctx context.Context, id uuid.UUID) (bool, error) {
			return false, nil
		},
	}

	u, err := bindUser(context.Background(), repo, &profile{UserID: "U-3", DisplayName: "Display", Email: "exists2@example.com"})
	if err != nil {
		t.Fatalf("bindUser returned error: %v", err)
	}
	if u == nil || u.ID != userID {
		t.Fatalf("expected email user ID %s, got %+v", userID.String(), u)
	}
	if repo.createLineBindingCalled != 1 {
		t.Fatalf("expected one line binding creation, got %d", repo.createLineBindingCalled)
	}
	if repo.createUserCalled != 0 {
		t.Fatalf("expected no user creation, got %d", repo.createUserCalled)
	}
}

// 案例：email 空白時，應建立 fallback email 的新 user，並建立 line 綁定。
func TestBindUser_EmptyEmail_CreatesUserWithFallbackEmail(t *testing.T) {
	newID := uuid.New()
	var createdEmail string
	var gotEmailPtr *string
	repo := &mockLineBindRepo{
		getUserByLineUserIDFn: func(ctx context.Context, lineUserID string) (*ent.User, error) {
			return nil, &ent.NotFoundError{}
		},
		getUserByEmailFn: func(ctx context.Context, email string) (*ent.User, error) {
			return nil, &ent.NotFoundError{}
		},
		createUserFn: func(ctx context.Context, name, email string) (*ent.User, error) {
			createdEmail = email
			return &ent.User{ID: newID, Name: name, Email: email}, nil
		},
		createLineBindingFn: func(ctx context.Context, u *ent.User, lineUserID, displayName string, email, picture *string) error {
			gotEmailPtr = email
			return nil
		},
	}

	u, err := bindUser(context.Background(), repo, &profile{UserID: "U-4", DisplayName: "", Email: "   "})
	if err != nil {
		t.Fatalf("bindUser returned error: %v", err)
	}
	if u == nil || u.ID != newID {
		t.Fatalf("expected created user ID %s, got %+v", newID.String(), u)
	}
	if createdEmail != "line_U-4@line.local" {
		t.Fatalf("expected fallback email, got %q", createdEmail)
	}
	if gotEmailPtr != nil {
		t.Fatalf("expected nil line email for empty input, got %v", *gotEmailPtr)
	}
}

// 案例：repository 回傳一般錯誤時，bindUser 應原樣往上傳遞。
func TestBindUser_PropagatesRepositoryError(t *testing.T) {
	repo := &mockLineBindRepo{
		getUserByLineUserIDFn: func(ctx context.Context, lineUserID string) (*ent.User, error) {
			return nil, errors.New("db down")
		},
	}

	_, err := bindUser(context.Background(), repo, &profile{UserID: "U-5", DisplayName: "X", Email: "x@example.com"})
	if err == nil || err.Error() != "db down" {
		t.Fatalf("expected db down error, got %v", err)
	}
}
