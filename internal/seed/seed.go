package seed

import (
	"context"

	"assistant-api/internal/ent"
)

// Run 執行所有預設資料初始化流程。
func Run(ctx context.Context, client *ent.Client) error {
	// 依序執行各 seeder，任何一個失敗即中止。
	for _, seeder := range []func(context.Context, *ent.Client) error{
		seedUsers,
		seedActionCatalog,
	} {
		if err := seeder(ctx, client); err != nil {
			return err
		}
	}
	return nil
}
