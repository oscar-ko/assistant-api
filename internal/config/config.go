package config

import (
	"fmt"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/spf13/viper"
)

const defaultConfigPath = "configs/app.yml"

// configuration 對應設定檔結構，透過指標綁定到 package-level 全域變數。
type configuration struct {
	RunMode    *string           `mapstructure:"run_mode"`
	Log        *LogConfig        `mapstructure:"log"`
	Server     *ServerConfig     `mapstructure:"server"`
	Database   *DatabaseConfig   `mapstructure:"database"`
	PostgreSQL *PostgreSQLConfig `mapstructure:"postgresql"`
	AI         *AIConfig         `mapstructure:"ai"`
	Line       *LineConfig       `mapstructure:"line"`
	GraphQL    *GraphQLConfig    `mapstructure:"graphql"`
}

type LogConfig struct {
	Level      string `mapstructure:"level" yaml:"level"`
	Filename   string `mapstructure:"filename" yaml:"filename"`
	MaxSize    int    `mapstructure:"max_size" yaml:"max_size"`
	MaxAge     int    `mapstructure:"max_age" yaml:"max_age"`
	MaxBackups int    `mapstructure:"max_backups" yaml:"max_backups"`
}

type ServerConfig struct {
	Port string `mapstructure:"port" yaml:"port"`
}

type DatabaseConfig struct {
	AutoSchemaCreate bool `mapstructure:"auto_schema_create" yaml:"auto_schema_create"`
}

// AIConfig 集中管理 AI 相關子系統設定，依用途拆成三個子區塊：
// - LLMInteraction：依角色路由到指定 provider/model（問答、追問、決策）
// - Embedding：第一階段候選召回（recall）
// - Reranker：第二階段候選精排（precision）
type AIConfig struct {
	LLMInteraction LLMInteractionConfig `mapstructure:"llm_interaction" yaml:"llm_interaction"`
	Embedding      EmbeddingConfig      `mapstructure:"embedding" yaml:"embedding"`
	Reranker       RerankerConfig       `mapstructure:"reranker" yaml:"reranker"`
}

// LLMInteractionConfig 為角色導向的 LLM 互動設定。
type LLMInteractionConfig struct {
	// Decision 指定「最終 action 決策」使用的 profile 名稱。
	// 目前支援：
	// - local（使用本地 interaction service）
	// - chatgpt 下的 profile key（例如 default, low_temp）
	Decision string `mapstructure:"decision" yaml:"decision"`
	// Chat 指定「一般問答/追問」使用的 profile 名稱。
	// 規則與 Decision 相同。
	Chat    string            `mapstructure:"chat" yaml:"chat"`
	Local   LLMEndpointConfig `mapstructure:"local" yaml:"local"`
	ChatGPT ChatGPTConfig     `mapstructure:"chatgpt" yaml:"chatgpt"`
	// CommandConfidenceThreshold 決定 final action 信心值低於多少時，
	// 直接視為對話意圖（非指令 action）。0 代表關閉此門檻判斷。
	CommandConfidenceThreshold float64 `mapstructure:"command_confidence_threshold" yaml:"command_confidence_threshold"`
	// QuestionConfidenceThreshold 決定問答回覆信心值低於多少時，
	// 應改由其他 cloud LLM 回答會更合適。0 代表關閉此門檻判斷。
	QuestionConfidenceThreshold float64 `mapstructure:"question_confidence_threshold" yaml:"question_confidence_threshold"`
	// DecisionJSONRetryCount 決定 action decision 遇到 JSON 格式錯誤時最多重送幾次。
	// 0 代表不重送（只送第一次）。
	DecisionJSONRetryCount int `mapstructure:"decision_json_retry_count" yaml:"decision_json_retry_count"`
}

// LLMEndpointConfig 為統一 interaction 端點設定。
type LLMEndpointConfig struct {
	ServiceURL            string `mapstructure:"service_url" yaml:"service_url"`
	ServiceTimeoutSeconds int    `mapstructure:"service_timeout_seconds" yaml:"service_timeout_seconds"`
}

// ChatGPTConfig 為 ChatGPT/OpenAI 供應商設定。
type ChatGPTConfig struct {
	URL      string                        `mapstructure:"url" yaml:"url"`
	Token    string                        `mapstructure:"token" yaml:"token"`
	Profiles map[string]ChatGPTModelConfig `mapstructure:"profiles" yaml:"profiles"`
}

// ChatGPTModelConfig 描述單一 ChatGPT 模型 profile。
// 可定義是否支援特定參數，避免因模型參數不相容而回 400。
type ChatGPTModelConfig struct {
	ModelName      string   `mapstructure:"model_name" yaml:"model_name"`
	TimeoutSeconds int      `mapstructure:"timeout_seconds" yaml:"timeout_seconds"`
	MaxTokens      *int     `mapstructure:"max_tokens" yaml:"max_tokens"`
	Temperature    *float64 `mapstructure:"temperature" yaml:"temperature"`
}

// EmbeddingConfig 為第一階段候選召回使用的向量化服務設定。
type EmbeddingConfig struct {
	URL            string `mapstructure:"url" yaml:"url"`
	TimeoutSeconds int    `mapstructure:"timeout_seconds" yaml:"timeout_seconds"`
	MaxAttempts    int    `mapstructure:"max_attempts" yaml:"max_attempts"`
	RetryBackoffMS int    `mapstructure:"retry_backoff_ms" yaml:"retry_backoff_ms"`
	Path           string `mapstructure:"path" yaml:"path"`
	// RetrievalTopK 控制第一階段向量召回（top-k）最多取回幾筆候選。
	RetrievalTopK int `mapstructure:"retrieval_top_k" yaml:"retrieval_top_k"`
	// Alive*：探活快取與失敗冷卻策略，避免每次訊息都探活。
	AliveProbeIntervalMS   int `mapstructure:"alive_probe_interval_ms" yaml:"alive_probe_interval_ms"`
	AliveProbeTimeoutMS    int `mapstructure:"alive_probe_timeout_ms" yaml:"alive_probe_timeout_ms"`
	AliveSuccessTTLMS      int `mapstructure:"alive_success_ttl_ms" yaml:"alive_success_ttl_ms"`
	AliveFailureCooldownMS int `mapstructure:"alive_failure_cooldown_ms" yaml:"alive_failure_cooldown_ms"`
}

// RerankerConfig 參數控制第二階段 cross-encoder 候選精排服務。
// 第一階段召回仍由 Embedding + pgvector 負責，兩者分工如下：
// - Embedding: 召回候選（recall）
// - Reranker: 精排候選（precision）
// - Enabled: 可切換是否啟用第二階段
type RerankerConfig struct {
	Enabled        bool   `mapstructure:"enabled" yaml:"enabled"`
	URL            string `mapstructure:"url" yaml:"url"`
	TimeoutSeconds int    `mapstructure:"timeout_seconds" yaml:"timeout_seconds"`
	MaxAttempts    int    `mapstructure:"max_attempts" yaml:"max_attempts"`
	RetryBackoffMS int    `mapstructure:"retry_backoff_ms" yaml:"retry_backoff_ms"`
	Path           string `mapstructure:"path" yaml:"path"`
	// TopK 控制第二階段 cross-encoder 精排最多回傳幾筆候選。
	TopK int `mapstructure:"top_k" yaml:"top_k"`
	// Alive*：探活快取與失敗冷卻策略，避免每次訊息都探活。
	AliveProbeIntervalMS   int `mapstructure:"alive_probe_interval_ms" yaml:"alive_probe_interval_ms"`
	AliveProbeTimeoutMS    int `mapstructure:"alive_probe_timeout_ms" yaml:"alive_probe_timeout_ms"`
	AliveSuccessTTLMS      int `mapstructure:"alive_success_ttl_ms" yaml:"alive_success_ttl_ms"`
	AliveFailureCooldownMS int `mapstructure:"alive_failure_cooldown_ms" yaml:"alive_failure_cooldown_ms"`
}

// LineConfig 為 LINE OAuth 綁定所需參數。
type LineConfig struct {
	ChannelToken    string `mapstructure:"channel_token" yaml:"channel_token"`
	ChannelSecret   string `mapstructure:"channel_secret" yaml:"channel_secret"`
	ChannelID       string `mapstructure:"channel_id" yaml:"channel_id"`
	BotUserID       string `mapstructure:"bot_user_id" yaml:"bot_user_id"`
	ClientSecret    string `mapstructure:"client_secret" yaml:"client_secret"`
	RedirectURI     string `mapstructure:"redirect_uri" yaml:"redirect_uri"`
	AssistantBotURL string `mapstructure:"assistant_bot_url" yaml:"assistant_bot_url"`
	Scopes          string `mapstructure:"scopes" yaml:"scopes"`
}

// PostgreSQLConfig 參照 backend 風格，集中管理 PostgreSQL 連線參數。
type PostgreSQLConfig struct {
	Address    string `mapstructure:"address" yaml:"address"`
	Database   string `mapstructure:"database" yaml:"database"`
	UserName   string `mapstructure:"user_name" yaml:"user_name"`
	Password   string `mapstructure:"password" yaml:"password"`
	Parameters string `mapstructure:"parameters" yaml:"parameters"`
}

// GetDSN 產出 Ent/PostgreSQL 可用的 DSN 字串。
func (p PostgreSQLConfig) GetDSN() string {
	if strings.TrimSpace(p.Address) == "" || strings.TrimSpace(p.Database) == "" {
		return ""
	}
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(p.UserName, p.Password),
		Host:     p.Address,
		Path:     p.Database,
		RawQuery: p.Parameters,
	}
	return dsn.String()
}

type GraphQLConfig struct {
	QueryPath      string `mapstructure:"query_path" yaml:"query_path"`
	PlaygroundPath string `mapstructure:"playground_path" yaml:"playground_path"`
}

var (
	once sync.Once

	// 以下為全域設定值，載入後可在各模組直接讀取。
	RunMode    string
	Log        LogConfig
	Server     ServerConfig
	Database   DatabaseConfig
	PostgreSQL PostgreSQLConfig
	AI         AIConfig
	Line       LineConfig
	GraphQL    GraphQLConfig

	config = &configuration{
		RunMode:    &RunMode,
		Log:        &Log,
		Server:     &Server,
		Database:   &Database,
		PostgreSQL: &PostgreSQL,
		AI:         &AI,
		Line:       &Line,
		GraphQL:    &GraphQL,
	}
)

// MustLoad 只執行一次設定初始化，行為與 backend 專案一致。
func MustLoad() {
	once.Do(func() {
		// 若有 APP_CONFIG，優先使用指定路徑；否則嘗試預設搜尋路徑。
		path := os.Getenv("APP_CONFIG")
		if path != "" {
			viper.SetConfigFile(path)
		} else {
			// 預設策略：優先讀專案 configs/app.yml，並保留多層路徑容錯。
			viper.SetConfigFile(defaultConfigPath)
			viper.SetConfigName("app")
			viper.SetConfigType("yaml")
			viper.AddConfigPath(".")
			viper.AddConfigPath("./configs")
			viper.AddConfigPath("../../configs")
			viper.AddConfigPath("../../")
		}

		viper.AutomaticEnv()
		// 將巢狀 key（如 line.channel_id）對應為環境變數格式（LINE_CHANNEL_ID）。
		viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

		// 預設值可讓本機開發在最少設定下啟動。
		viper.SetDefault("run_mode", "dev")
		viper.SetDefault("log.level", "info")
		viper.SetDefault("log.filename", "")
		viper.SetDefault("log.max_size", 10)
		viper.SetDefault("log.max_age", 7)
		viper.SetDefault("log.max_backups", 5)
		viper.SetDefault("server.port", "8080")
		viper.SetDefault("database.auto_schema_create", true)
		viper.SetDefault("postgresql.address", "")
		viper.SetDefault("postgresql.database", "")
		viper.SetDefault("postgresql.user_name", "")
		viper.SetDefault("postgresql.password", "")
		viper.SetDefault("postgresql.parameters", "sslmode=disable")
		viper.SetDefault("ai.llm_interaction.decision", "local")
		viper.SetDefault("ai.llm_interaction.chat", "local")
		viper.SetDefault("ai.llm_interaction.local.service_url", "http://127.0.0.1:9002")
		viper.SetDefault("ai.llm_interaction.local.service_timeout_seconds", 90)
		viper.SetDefault("ai.llm_interaction.chatgpt.url", "https://api.openai.com/v1")
		viper.SetDefault("ai.llm_interaction.chatgpt.token", "")
		viper.SetDefault("ai.llm_interaction.chatgpt.profiles.default.model_name", "gpt-4o-mini")
		viper.SetDefault("ai.llm_interaction.chatgpt.profiles.default.timeout_seconds", 120)
		viper.SetDefault("ai.llm_interaction.chatgpt.profiles.default.max_tokens", 1024)
		viper.SetDefault("ai.llm_interaction.chatgpt.profiles.default.temperature", 0.2)
		// 第一層門檻：action decision confidence 低於此值時，
		// 不直接執行 action，改走 question-answer 分支。
		viper.SetDefault("ai.llm_interaction.command_confidence_threshold", 0.7)
		// 第二層門檻：question-answer confidence 低於此值時，
		// 標記建議改送 cloud LLM（例如時事/高難推理問題）。
		viper.SetDefault("ai.llm_interaction.question_confidence_threshold", 0.6)
		// action decision JSON 格式錯誤重送次數。
		viper.SetDefault("ai.llm_interaction.decision_json_retry_count", 2)
		viper.SetDefault("ai.embedding.url", "http://127.0.0.1:9000")
		viper.SetDefault("ai.embedding.timeout_seconds", 60)
		viper.SetDefault("ai.embedding.max_attempts", 4)
		viper.SetDefault("ai.embedding.retry_backoff_ms", 500)
		viper.SetDefault("ai.embedding.path", "/embed")
		viper.SetDefault("ai.embedding.alive_probe_interval_ms", 2000)
		viper.SetDefault("ai.embedding.alive_probe_timeout_ms", 1500)
		viper.SetDefault("ai.embedding.alive_success_ttl_ms", 10000)
		viper.SetDefault("ai.embedding.alive_failure_cooldown_ms", 3000)
		// 第一階段向量召回預設取回 20 筆候選，確保正確候選不會被漏掉，
		// 再交給 reranker 精排縮減到最終筆數。
		viper.SetDefault("ai.embedding.retrieval_top_k", 20)
		// cross-encoder reranker 的預設本機端點（第二階段重排）。
		// 這些預設值可讓本機在未特別覆寫時，直接對接 9001 服務。
		viper.SetDefault("ai.reranker.enabled", true)
		viper.SetDefault("ai.reranker.url", "http://127.0.0.1:9001")
		viper.SetDefault("ai.reranker.timeout_seconds", 60)
		viper.SetDefault("ai.reranker.max_attempts", 3)
		viper.SetDefault("ai.reranker.retry_backoff_ms", 300)
		viper.SetDefault("ai.reranker.path", "/rerank")
		// 第二階段精排預設回傳 5 筆候選，維持與召回筆數一致。
		viper.SetDefault("ai.reranker.top_k", 5)
		viper.SetDefault("ai.reranker.alive_probe_interval_ms", 2000)
		viper.SetDefault("ai.reranker.alive_probe_timeout_ms", 1500)
		viper.SetDefault("ai.reranker.alive_success_ttl_ms", 10000)
		viper.SetDefault("ai.reranker.alive_failure_cooldown_ms", 3000)
		viper.SetDefault("line.channel_token", "")
		viper.SetDefault("line.channel_secret", "")
		viper.SetDefault("line.channel_id", "")
		viper.SetDefault("line.bot_user_id", "")
		viper.SetDefault("line.client_secret", "")
		viper.SetDefault("line.redirect_uri", "")
		viper.SetDefault("line.assistant_bot_url", "")
		viper.SetDefault("line.scopes", "openid profile email")
		viper.SetDefault("graphql.query_path", "/query")
		viper.SetDefault("graphql.playground_path", "/playground")

		if err := viper.ReadInConfig(); err != nil {
			// APP_CONFIG 未指定時，輸出通用錯誤訊息。
			if path == "" {
				log.Fatalf("failed to read config file: %v", err)
			}
			// APP_CONFIG 有指定時，回報完整路徑方便排錯。
			log.Fatalf("failed to read config file %q: %v", path, err)
		}

		// 重要欄位缺失時直接中止，避免服務以不完整設定啟動。
		requiredKeys := []string{
			"server.port",
			"postgresql.address",
			"postgresql.database",
			"graphql.query_path",
			"graphql.playground_path",
		}
		for _, key := range requiredKeys {
			// required key 缺失即中止啟動，避免 runtime 才爆設定錯誤。
			if !viper.IsSet(key) {
				log.Fatalf("%v", fmt.Errorf("missing required config key: %s", key))
			}
		}

		if err := viper.Unmarshal(config); err != nil {
			log.Fatalf("failed to parse config: %v", err)
		}

		// 補齊空值容錯，避免 scope 留空導致 OAuth 行為異常。
		if strings.TrimSpace(Line.Scopes) == "" {
			Line.Scopes = "openid profile email"
		}
	})
}
