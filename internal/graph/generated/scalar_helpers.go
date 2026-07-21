package generated

// 本檔提供 UUID 與 Vector 這兩個自訂 GraphQL scalar 的手寫編碼/解碼邏輯。
//
// gqlgen 對於非內建（built-in）scalar，只會在 gqlgen.yml 的 models 對應後產生介面/接線碼，
// 不會自動產生實際的 marshal/unmarshal 邏輯，因此需要手動補上對應的
// `_TypeName` 與 `unmarshalInputTypeName` 方法，才能讓執行引擎知道如何處理這兩種型別。
// 写法上並行於既有 ID scalar 的处理方式（見 internal/graph/scalars_uuid.go 與 internal/graph/generated/id_helpers.go）。
import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
	"github.com/vektah/gqlparser/v2/ast"
)

// unmarshalInputUUID 將 GraphQL 輸入值解碼成 uuid.UUID。
// UUID scalar 在型別上與內建的 ID scalar 完全相同（同樣是 uuid.UUID），
// 所以直接候提既有的 unmarshalInputID，避免重複實作一份解碼邏輯。
func (ec *executionContext) unmarshalInputUUID(ctx context.Context, v any) (uuid.UUID, error) {
	return ec.unmarshalInputID(ctx, v)
}

// _UUID 將 uuid.UUID 編碼回 GraphQL 回傳值，同樣候提 ID scalar 的編碼邏輯。
func (ec *executionContext) _UUID(ctx context.Context, sel ast.SelectionSet, v *uuid.UUID) graphql.Marshaler {
	return ec._ID(ctx, sel, v)
}

// unmarshalInputVector 將 GraphQL 輸入值解碼成 pgvector.Vector，支援三種輸入型式：
//  1. 已經是 pgvector.Vector（例如內部直接傳递）：直接回傳。
//  2. 字串（例如 "[0.1,0.2,0.3]"，pgvector 在 Postgres 的存儲/顯示格式）：交由 parseGraphQLVector 解析。
//  3. 數字陣列（GraphQL 查詢中直接寫 [0.1, 0.2, 0.3]）：逐項用 graphql.UnmarshalFloat 解析。
//
// 其他未知型別則先嘗試當作字串解碼後再證向量，確保盡可能寬容不同來源的輸入。
func (ec *executionContext) unmarshalInputVector(ctx context.Context, v any) (pgvector.Vector, error) {
	_ = ctx
	switch value := v.(type) {
	case pgvector.Vector:
		return value, nil
	case string:
		return parseGraphQLVector(value)
	case []any:
		items := make([]float32, 0, len(value))
		for _, item := range value {
			parsed, err := graphql.UnmarshalFloat(item)
			if err != nil {
				return pgvector.Vector{}, err
			}
			items = append(items, float32(parsed))
		}
		return pgvector.NewVector(items), nil
	default:
		raw, err := graphql.UnmarshalString(v)
		if err != nil {
			return pgvector.Vector{}, err
		}
		return parseGraphQLVector(raw)
	}
}

// _Vector 將 pgvector.Vector 編碼回 GraphQL 回傳值。
// 目前統一輸出成字串格式（例如 "[0.1,0.2,0.3]"），與 Postgres pgvector 顯示格式保持一致。
func (ec *executionContext) _Vector(ctx context.Context, sel ast.SelectionSet, v *pgvector.Vector) graphql.Marshaler {
	_ = ec
	_ = ctx
	_ = sel
	if v == nil {
		return graphql.Null
	}
	return graphql.MarshalString(v.String())
}

// parseGraphQLVector 解析 "[0.1,0.2,0.3]" 格式的字串成 pgvector.Vector。
// 空字串（去除 [] 之後為空）視為空向量，非法數值直接回錯，不做預設值 fallback。
func parseGraphQLVector(raw string) (pgvector.Vector, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "[")
	trimmed = strings.TrimSuffix(trimmed, "]")
	if strings.TrimSpace(trimmed) == "" {
		return pgvector.NewVector(nil), nil
	}
	parts := strings.Split(trimmed, ",")
	items := make([]float32, 0, len(parts))
	for _, part := range parts {
		parsed, err := strconv.ParseFloat(strings.TrimSpace(part), 32)
		if err != nil {
			return pgvector.Vector{}, fmt.Errorf("invalid vector value %q: %w", part, err)
		}
		items = append(items, float32(parsed))
	}
	return pgvector.NewVector(items), nil
}
