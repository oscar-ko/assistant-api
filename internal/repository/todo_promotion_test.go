package repository

import (
	"testing"
	"time"

	"assistant-api/internal/ent"
	"assistant-api/internal/ent/todocandidate"
)

func TestTodoCandidatePromotionReadyGate(t *testing.T) {
	// promotion gate 是 Candidate -> Todo 的資料完整性邊界：
	// analyzer 可以先產生 candidate，但正式 Todo 必須至少具備可顯示摘要與已正規化 due_at。
	dueAt := time.Now()
	ready := &ent.TodoCandidate{Status: todocandidate.StatusCandidate, Summary: "整理原因", DueAt: &dueAt}
	if !isTodoCandidatePromotionReady(ready) {
		t.Fatal("expected candidate with summary and due_at to be promotion-ready")
	}

	// missing_fields 代表 analyzer 或 normalizer 明確說資料仍不完整；
	// 此時即使 summary/due_at 看似存在，也不能建立正式 Todo，避免把半成品推進提醒系統。
	blockedByMissingFields := &ent.TodoCandidate{Status: todocandidate.StatusCandidate, Summary: "整理原因", DueAt: &dueAt, MissingFields: []string{"assignees"}}
	if isTodoCandidatePromotionReady(blockedByMissingFields) {
		t.Fatal("expected candidate with missing_fields to be blocked")
	}

	// needs_more_info 是 candidate 狀態機的明確暫停狀態；
	// promotion 不應繞過狀態機自行猜測缺失欄位已可接受。
	blockedByStatus := &ent.TodoCandidate{Status: todocandidate.StatusNeedsMoreInfo, Summary: "整理原因", DueAt: &dueAt}
	if isTodoCandidatePromotionReady(blockedByStatus) {
		t.Fatal("expected needs_more_info candidate to be blocked")
	}

	// due_at 是正式 Todo 可提醒的最低要求；只有 due_text 但未正規化時，仍停留在 candidate。
	blockedByDueAt := &ent.TodoCandidate{Status: todocandidate.StatusCandidate, Summary: "整理原因"}
	if isTodoCandidatePromotionReady(blockedByDueAt) {
		t.Fatal("expected candidate without due_at to be blocked")
	}
}
