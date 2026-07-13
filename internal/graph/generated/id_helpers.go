package generated

import (
	"context"
	"fmt"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
	"github.com/vektah/gqlparser/v2/ast"
)

// unmarshalInputID parses GraphQL ID input into uuid.UUID.
func (ec *executionContext) unmarshalInputID(ctx context.Context, v any) (uuid.UUID, error) {
	_ = ctx
	switch value := v.(type) {
	case uuid.UUID:
		return value, nil
	case string:
		parsed, err := uuid.Parse(strings.TrimSpace(value))
		if err != nil {
			return uuid.Nil, fmt.Errorf("invalid id uuid: %w", err)
		}
		return parsed, nil
	default:
		raw, err := graphql.UnmarshalString(v)
		if err != nil {
			return uuid.Nil, err
		}
		parsed, err := uuid.Parse(strings.TrimSpace(raw))
		if err != nil {
			return uuid.Nil, fmt.Errorf("invalid id uuid: %w", err)
		}
		return parsed, nil
	}
}

// _ID marshals uuid.UUID as GraphQL ID string.
func (ec *executionContext) _ID(ctx context.Context, sel ast.SelectionSet, v *uuid.UUID) graphql.Marshaler {
	_ = ec
	_ = ctx
	_ = sel
	if v == nil {
		return graphql.Null
	}
	return graphql.MarshalString(v.String())
}
