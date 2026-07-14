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

type AIConfig struct {
	// SemanticDecision*：語義決策服務（意圖/決策）端點設定。
	SemanticDecisionServiceURL            string `mapstructure:"semantic_decision_service_url" yaml:"semantic_decision_service_url"`
	SemanticDecisionServiceTimeoutSeconds int    `mapstructure:"semantic_decision_service_timeout_seconds" yaml:"semantic_decision_service_timeout_seconds"`
	// Embedding*：第一階段候選召回使用的向量化服務設定。
	EmbeddingURL            string `mapstructure:"embedding_url" yaml:"embedding_url"`
	EmbeddingTimeoutSeconds int    `mapstructure:"embedding_timeout_seconds" yaml:"embedding_timeout_seconds"`
	EmbeddingMaxAttempts    int    `mapstructure:"embedding_max_attempts" yaml:"embedding_max_attempts"`
	EmbeddingRetryBackoffMS int    `mapstructure:"embedding_retry_backoff_ms" yaml:"embedding_retry_backoff_ms"`
	EmbeddingPath           string `mapstructure:"embedding_path" yaml:"embedding_path"`
	// EmbeddingAlive*：探活快取與失敗冷卻策略，避免每次訊息都探活。
	EmbeddingAliveProbeIntervalMS   int `mapstructure:"embedding_alive_probe_interval_ms" yaml:"embedding_alive_probe_interval_ms"`
	EmbeddingAliveProbeTimeoutMS    int `mapstructure:"embedding_alive_probe_timeout_ms" yaml:"embedding_alive_probe_timeout_ms"`
	EmbeddingAliveSuccessTTLMS      int `mapstructure:"embedding_alive_success_ttl_ms" yaml:"embedding_alive_success_ttl_ms"`
	EmbeddingAliveFailureCooldownMS int `mapstructure:"embedding_alive_failure_cooldown_ms" yaml:"embedding_alive_failure_cooldown_ms"`
	// Reranker* 參數控制第二階段 cross-encoder 候選精排服務。
	// 第一階段召回仍由 embedding + pgvector 負責，兩者分工如下：
	// - Embedding*: 召回候選（recall）
	// - Reranker*: 精排候選（precision）
	// - RerankerEnabled: 可切換是否啟用第二階段
	RerankerEnabled        bool   `mapstructure:"reranker_enabled" yaml:"reranker_enabled"`
	RerankerURL            string `mapstructure:"reranker_url" yaml:"reranker_url"`
	RerankerTimeoutSeconds int    `mapstructure:"reranker_timeout_seconds" yaml:"reranker_timeout_seconds"`
	RerankerMaxAttempts    int    `mapstructure:"reranker_max_attempts" yaml:"reranker_max_attempts"`
	RerankerRetryBackoffMS int    `mapstructure:"reranker_retry_backoff_ms" yaml:"reranker_retry_backoff_ms"`
	RerankerPath           string `mapstructure:"reranker_path" yaml:"reranker_path"`
	// RerankerAlive*：探活快取與失敗冷卻策略，避免每次訊息都探活。
	RerankerAliveProbeIntervalMS   int `mapstructure:"reranker_alive_probe_interval_ms" yaml:"reranker_alive_probe_interval_ms"`
	RerankerAliveProbeTimeoutMS    int `mapstructure:"reranker_alive_probe_timeout_ms" yaml:"reranker_alive_probe_timeout_ms"`
	RerankerAliveSuccessTTLMS      int `mapstructure:"reranker_alive_success_ttl_ms" yaml:"reranker_alive_success_ttl_ms"`
	RerankerAliveFailureCooldownMS int `mapstructure:"reranker_alive_failure_cooldown_ms" yaml:"reranker_alive_failure_cooldown_ms"`
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
		viper.SetDefault("ai.semantic_decision_service_url", "http://127.0.0.1:9002")
		viper.SetDefault("ai.semantic_decision_service_timeout_seconds", 90)
		viper.SetDefault("ai.embedding_url", "http://127.0.0.1:9000")
		viper.SetDefault("ai.embedding_timeout_seconds", 60)
		viper.SetDefault("ai.embedding_max_attempts", 4)
		viper.SetDefault("ai.embedding_retry_backoff_ms", 500)
		viper.SetDefault("ai.embedding_path", "/embed")
		viper.SetDefault("ai.embedding_alive_probe_interval_ms", 2000)
		viper.SetDefault("ai.embedding_alive_probe_timeout_ms", 1500)
		viper.SetDefault("ai.embedding_alive_success_ttl_ms", 10000)
		viper.SetDefault("ai.embedding_alive_failure_cooldown_ms", 3000)
		// cross-encoder reranker 的預設本機端點（第二階段重排）。
		// 這些預設值可讓本機在未特別覆寫時，直接對接 9001 服務。
		viper.SetDefault("ai.reranker_enabled", true)
		viper.SetDefault("ai.reranker_url", "http://127.0.0.1:9001")
		viper.SetDefault("ai.reranker_timeout_seconds", 60)
		viper.SetDefault("ai.reranker_max_attempts", 3)
		viper.SetDefault("ai.reranker_retry_backoff_ms", 300)
		viper.SetDefault("ai.reranker_path", "/rerank")
		viper.SetDefault("ai.reranker_alive_probe_interval_ms", 2000)
		viper.SetDefault("ai.reranker_alive_probe_timeout_ms", 1500)
		viper.SetDefault("ai.reranker_alive_success_ttl_ms", 10000)
		viper.SetDefault("ai.reranker_alive_failure_cooldown_ms", 3000)
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
