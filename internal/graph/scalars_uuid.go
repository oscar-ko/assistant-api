package graph

import (
	"fmt"
	"strings"

	"github.com/99designs/gqlgen/graphql"
	"github.com/google/uuid"
)

// MarshalID serializes GraphQL ID values as UUID strings.
func MarshalID(value uuid.UUID) graphql.Marshaler {
	return graphql.MarshalString(value.String())
}

// UnmarshalID parses GraphQL ID input into uuid.UUID.
func UnmarshalID(input interface{}) (uuid.UUID, error) {
	raw, ok := input.(string)
	if !ok {
		return uuid.Nil, fmt.Errorf("id must be a string")
	}
	parsed, err := uuid.Parse(strings.TrimSpace(raw))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid id uuid: %w", err)
	}
	return parsed, nil
}
