//go:build cgo

package repository

import (
	"context"
	"testing"

	"assistant-api/internal/ent/enttest"

	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
)

func TestServiceMemberGatingUsesBoundInternalUserID(t *testing.T) {
	// 這個測試鎖住即時翻譯曾經壞掉的資料邊界：
	// 1. LINE/Slack 的 platform_user_id 只能從平台綁定表解析。
	// 2. channel_service_members 只保存 internal user_id 與 skill 啟用狀態。
	// 3. realtime gating 必須先完成平台身分解析，再用 internal user_id 查啟用狀態。
	ctx := context.Background()
	client := enttest.Open(t, "sqlite3", "file:service_member_identity?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })

	repo := NewChannelMessageRepo(client)
	owner := client.User.Create().
		SetName("翻譯使用者").
		SetEmail("translator@example.com").
		SaveX(ctx)
	client.Line.Create().
		SetPlatformUserID("line-user-1").
		SetUserID(owner.ID).
		SaveX(ctx)
	channel := client.Channel.Create().
		SetName("line-group").
		SetPlatform("line").
		SetGroupID("group-1").
		SetType("group").
		SaveX(ctx)
	skill := client.Skill.Create().
		SetSkillCode("channel.translation").
		SetName("即時翻譯").
		SetIsRealtime(true).
		SaveX(ctx)

	if err := repo.AddServiceMemberToChannel(ctx, channel.ID, owner.ID, skill.ID); err != nil {
		t.Fatalf("AddServiceMemberToChannel() error = %v", err)
	}

	// 模擬 webhook 收到平台 user id 後，先走 binding table 解析成系統內 user id。
	resolvedOwnerID, err := repo.ResolveBoundUserIDByPlatformIdentity(ctx, "line", "", "line-user-1")
	if err != nil {
		t.Fatalf("ResolveBoundUserIDByPlatformIdentity() error = %v", err)
	}
	if resolvedOwnerID != owner.ID {
		t.Fatalf("ResolveBoundUserIDByPlatformIdentity() = %s, want %s", resolvedOwnerID, owner.ID)
	}

	// channel_service_members 的查詢鍵必須是 internal user id，不可拿平台 user id 直接查。
	enabled, err := repo.HasChannelServiceMember(ctx, channel.ID, resolvedOwnerID, skill.ID)
	if err != nil {
		t.Fatalf("HasChannelServiceMember() error = %v", err)
	}
	if !enabled {
		t.Fatal("HasChannelServiceMember() = false, want true for bound internal user id")
	}

	// 未綁定的平台 user id 回 uuid.Nil，讓上層明確略過即時翻譯，不做錯誤 fallback。
	unboundOwnerID, err := repo.ResolveBoundUserIDByPlatformIdentity(ctx, "line", "", "line-user-missing")
	if err != nil {
		t.Fatalf("ResolveBoundUserIDByPlatformIdentity() missing user error = %v", err)
	}
	if unboundOwnerID != uuid.Nil {
		t.Fatalf("ResolveBoundUserIDByPlatformIdentity() missing user = %s, want nil uuid", unboundOwnerID)
	}
}
