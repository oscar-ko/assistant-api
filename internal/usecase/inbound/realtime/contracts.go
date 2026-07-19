package realtime

import "context"

// Translator 定義即時翻譯 usecase 所需的翻譯能力。
//
// 這裡只描述 usecase 需要什麼，不描述底層是本地 HTTP、OpenAI 相容端點，
// 或其他外部服務。具體 adapter 應放在 integration 層實作。
type Translator interface {
	Translate(ctx context.Context, text string, targetLocales []string) (map[string]string, error)
}

// Classifier 定義非指令訊息分類 usecase 所需的分類能力。
//
// usecase 只關心分類結果，不關心模型服務 URL、prompt、labels 如何組裝。
type Classifier interface {
	Classify(ctx context.Context, text string) (*ClassificationResult, error)
}

// ClassificationResult 是分類 adapter 回傳給 usecase 的最小結果集合。
//
// Tag 是主要分流依據；Labels/Scores/ModelName 保留觀測與後續 handler 需要的上下文。
type ClassificationResult struct {
	Tag       string
	Labels    []string
	Scores    map[string]float64
	ModelName string
}
