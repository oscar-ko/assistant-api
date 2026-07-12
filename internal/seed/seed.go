package seed

import (
	"context"

	"assistant-api/internal/ent"
)

// Run 執行所有預設資料初始化流程。
func Run(ctx context.Context, client *ent.Client) error {
	for _, seeder := range []func(context.Context, *ent.Client) error{
		seedUsers,
	} {
		if err := seeder(ctx, client); err != nil {
			return err
		}
	}
	return nil
}
