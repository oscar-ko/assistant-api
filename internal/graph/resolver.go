package graph

import "assistant-api/internal/ent"

// Resolver keeps dependencies for GraphQL resolvers.
type Resolver struct {
	Client *ent.Client
}
