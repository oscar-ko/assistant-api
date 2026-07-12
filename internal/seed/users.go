package seed

import (
	"context"
	"log"

	"assistant-api/internal/ent"
	"assistant-api/internal/ent/user"
)

// seedUsers 初始化系統預設使用者資料（可重複執行）。
func seedUsers(ctx context.Context, client *ent.Client) error {
	defaults := []struct {
		Name  string
		Email string
	}{
		{Name: "Admin", Email: "admin@example.com"},
		{Name: "Demo User", Email: "demo@example.com"},
	}

	for _, u := range defaults {
		exists, err := client.User.Query().Where(user.EmailEQ(u.Email)).Exist(ctx)
		if err != nil {
			return err
		}
		if exists {
			continue
		}

		if _, err := client.User.Create().SetName(u.Name).SetEmail(u.Email).Save(ctx); err != nil {
			return err
		}
		log.Printf("seeded default user: %s", u.Email)
	}

	return nil
}
