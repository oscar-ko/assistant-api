package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode"

	"assistant-api/internal/ent"
	"assistant-api/internal/ent/action"
	"assistant-api/internal/ent/actionroute"
	"assistant-api/internal/ent/skill"

	"entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/pgvector/pgvector-go"
)

// ActionRouteRepo provides queries for action_route records used by vector retrieval.
type ActionRouteRepo struct {
	db *ent.Client
}

// ActionRouteVectorCandidate is a top-k candidate returned by pgvector retrieval.
type ActionRouteVectorCandidate struct {
	APIOperation string
	SkillCode    string
	RouteText    string
	Distance     float64
}

func NewActionRouteRepo(db *ent.Client) ActionRouteRepo {
	return ActionRouteRepo{db: db}
}

func (r ActionRouteRepo) FindAllWithEmbeddingByLocale(ctx context.Context, locale string) ([]*ent.ActionRoute, error) {
	return r.db.ActionRoute.
		Query().
		Where(
			actionroute.LocaleEQ(locale),
			actionroute.EmbeddingNotNil(),
		).
		WithAction().
		All(ctx)
}

func (r ActionRouteRepo) ListWithoutEmbedding(ctx context.Context) ([]*ent.ActionRoute, error) {
	return r.db.ActionRoute.
		Query().
		Where(actionroute.EmbeddingIsNil()).
		All(ctx)
}

func (r ActionRouteRepo) UpdateEmbedding(ctx context.Context, id uuid.UUID, embedding pgvector.Vector) error {
	_, err := r.db.ActionRoute.UpdateOneID(id).SetEmbedding(embedding).Save(ctx)
	return err
}

// SearchTopByVectorAndLocale performs pgvector top-k retrieval.
func (r ActionRouteRepo) SearchTopByVectorAndLocale(ctx context.Context, locale string, queryVector string, topK int, skillCodes []string) ([]ActionRouteVectorCandidate, error) {
	if strings.TrimSpace(locale) == "" {
		return nil, fmt.Errorf("locale is required")
	}
	if strings.TrimSpace(queryVector) == "" {
		return nil, fmt.Errorf("query vector is required")
	}
	if topK <= 0 {
		topK = 5
	}

	queryVec, parseErr := parseEmbeddingVector(queryVector)
	if parseErr != nil {
		return nil, fmt.Errorf("failed to parse query vector: %w", parseErr)
	}

	q := r.db.ActionRoute.
		Query().
		Where(
			actionroute.LocaleEQ(strings.TrimSpace(locale)),
			actionroute.EmbeddingNotNil(),
		).
		WithAction(func(aq *ent.ActionQuery) {
			aq.WithSkill()
		})

	if len(skillCodes) > 0 {
		filtered := make([]string, 0, len(skillCodes))
		for _, code := range skillCodes {
			code = strings.TrimSpace(code)
			if code != "" {
				filtered = append(filtered, code)
			}
		}
		if len(filtered) > 0 {
			q = q.Where(
				actionroute.HasActionWith(
					action.HasSkillWith(
						skill.SkillCodeIn(filtered...),
					),
				),
			)
		}
	}

	queryVectorLiteral := strings.TrimSpace(queryVector)
	q = q.Order(func(s *sql.Selector) {
		// BGE-M3 retrieval works better with cosine distance than L2.
		s.OrderExpr(sql.ExprP(fmt.Sprintf("embedding <=> '%s'::vector", queryVectorLiteral)))
	}).Limit(topK)

	routes, err := q.All(ctx)
	if err != nil {
		return nil, err
	}

	candidates := make([]ActionRouteVectorCandidate, 0, len(routes))
	for _, route := range routes {
		if route.Embedding == nil || route.Edges.Action == nil || route.Edges.Action.Edges.Skill == nil {
			continue
		}
		distance := cosineDistance(route.Embedding.Slice(), queryVec)
		candidates = append(candidates, ActionRouteVectorCandidate{
			APIOperation: route.Edges.Action.APIOperation,
			SkillCode:    route.Edges.Action.Edges.Skill.SkillCode,
			RouteText:    strings.TrimSpace(route.RouteText),
			Distance:     distance,
		})
	}

	return candidates, nil
}

func (r ActionRouteRepo) FindBestOperationByRouteText(ctx context.Context, locale string, message string, candidateOperations []string) (string, error) {
	if r.db == nil {
		return "", fmt.Errorf("action route repo db is nil")
	}

	locale = strings.TrimSpace(locale)
	message = strings.TrimSpace(message)
	if locale == "" || message == "" {
		return "", nil
	}

	filtered := make([]string, 0, len(candidateOperations))
	for _, operation := range candidateOperations {
		value := strings.TrimSpace(operation)
		if value != "" {
			filtered = append(filtered, value)
		}
	}
	if len(filtered) == 0 {
		return "", nil
	}

	routes, err := r.db.ActionRoute.Query().
		Where(
			actionroute.LocaleEQ(locale),
			actionroute.HasActionWith(action.APIOperationIn(filtered...)),
		).
		WithAction().
		All(ctx)
	if err != nil {
		return "", err
	}

	messageFolded := strings.ToLower(message)
	messageNorm := normalizeRouteTextForMatch(message)
	bestOperation := ""
	bestScore := 0.0
	bestRouteLen := 0
	const minScore = 0.18
	for _, route := range routes {
		if route == nil || route.Edges.Action == nil {
			continue
		}
		routeText := strings.TrimSpace(route.RouteText)
		if routeText == "" {
			continue
		}
		routeFolded := strings.ToLower(routeText)
		routeNorm := normalizeRouteTextForMatch(routeText)

		score := routeTextMatchScore(message, messageFolded, messageNorm, routeText, routeFolded, routeNorm)
		if score < minScore {
			continue
		}

		if score > bestScore || (math.Abs(score-bestScore) < 1e-9 && len(routeText) > bestRouteLen) {
			bestScore = score
			bestRouteLen = len(routeText)
			bestOperation = strings.TrimSpace(route.Edges.Action.APIOperation)
		}
	}

	return bestOperation, nil
}

func (r ActionRouteRepo) ListCommandPurposeByOperations(ctx context.Context, locale string, operations []string) (map[string]string, error) {
	if r.db == nil {
		return map[string]string{}, fmt.Errorf("action route repo db is nil")
	}

	filtered := make([]string, 0, len(operations))
	for _, operation := range operations {
		value := strings.TrimSpace(operation)
		if value != "" {
			filtered = append(filtered, value)
		}
	}
	if len(filtered) == 0 {
		return map[string]string{}, nil
	}

	actions, err := r.db.Action.Query().
		Where(action.APIOperationIn(filtered...)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := map[string]string{}
	for _, item := range actions {
		if item == nil {
			continue
		}
		op := strings.TrimSpace(item.APIOperation)
		if op == "" {
			continue
		}
		purpose := ""
		if item.CommandPurpose != nil {
			purpose = strings.TrimSpace(*item.CommandPurpose)
		}
		if purpose == "" {
			continue
		}
		if _, exists := out[op]; exists {
			continue
		}
		out[op] = purpose
	}

	return out, nil
}

func normalizeRouteTextForMatch(text string) string {
	trimmed := strings.TrimSpace(strings.ToLower(text))
	if trimmed == "" {
		return ""
	}

	b := strings.Builder{}
	for _, r := range trimmed {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		b.WriteRune(r)
	}

	return b.String()
}

func routeTextMatchScore(message string, messageFolded string, messageNorm string, routeText string, routeFolded string, routeNorm string) float64 {
	if routeText == "" || message == "" {
		return 0
	}

	if strings.Contains(message, routeText) || strings.Contains(messageFolded, routeFolded) {
		return 1.0
	}

	if routeNorm != "" && strings.Contains(messageNorm, routeNorm) {
		return 0.95
	}

	return runeSetDice(messageNorm, routeNorm)
}

func runeSetDice(a string, b string) float64 {
	if a == "" || b == "" {
		return 0
	}

	setA := map[rune]struct{}{}
	for _, r := range a {
		setA[r] = struct{}{}
	}
	setB := map[rune]struct{}{}
	for _, r := range b {
		setB[r] = struct{}{}
	}

	if len(setA) == 0 || len(setB) == 0 {
		return 0
	}

	intersect := 0
	for r := range setA {
		if _, ok := setB[r]; ok {
			intersect++
		}
	}

	return (2.0 * float64(intersect)) / float64(len(setA)+len(setB))
}

func parseEmbeddingVector(s string) ([]float32, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("empty embedding string")
	}
	var vec64 []float64
	if err := json.Unmarshal([]byte(s), &vec64); err != nil {
		return nil, err
	}
	vec := make([]float32, 0, len(vec64))
	for _, v := range vec64 {
		vec = append(vec, float32(v))
	}
	return vec, nil
}

func cosineDistance(a, b []float32) float64 {
	if len(a) == 0 || len(b) == 0 {
		return math.Inf(1)
	}
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var dot float64
	var normA float64
	var normB float64
	for i := 0; i < n; i++ {
		av := float64(a[i])
		bv := float64(b[i])
		dot += av * bv
		normA += av * av
		normB += bv * bv
	}
	if len(a) != len(b) {
		return math.Inf(1)
	}
	if normA == 0 || normB == 0 {
		return math.Inf(1)
	}
	cosineSim := dot / (math.Sqrt(normA) * math.Sqrt(normB))
	return 1.0 - cosineSim
}
