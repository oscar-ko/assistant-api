package line

import (
	"context"
	"time"
)

// MemberNameCache 定義可替換的成員名稱快取介面。
// 目前先提供 no-op 實作，後續可替換為 Redis/DB-backed cache。
type MemberNameCache interface {
	Get(ctx context.Context, platform string, channelID string, channelType string, userID string) (displayName string, expiresAt time.Time, found bool, err error)
	Set(ctx context.Context, platform string, channelID string, channelType string, userID string, displayName string, expiresAt time.Time) error
}

// NoopMemberNameCache 為預設快取實作，不會儲存任何資料。
type NoopMemberNameCache struct{}

func (NoopMemberNameCache) Get(ctx context.Context, platform string, channelID string, channelType string, userID string) (string, time.Time, bool, error) {
	return "", time.Time{}, false, nil
}

func (NoopMemberNameCache) Set(ctx context.Context, platform string, channelID string, channelType string, userID string, displayName string, expiresAt time.Time) error {
	return nil
}
