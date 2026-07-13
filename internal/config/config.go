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
	SemanticDecisionServiceURL            string `mapstructure:"semantic_decision_service_url" yaml:"semantic_decision_service_url"`
	SemanticDecisionServiceTimeoutSeconds int    `mapstructure:"semantic_decision_service_timeout_seconds" yaml:"semantic_decision_service_timeout_seconds"`
	EmbeddingURL                          string `mapstructure:"embedding_url" yaml:"embedding_url"`
	EmbeddingTimeoutSeconds               int    `mapstructure:"embedding_timeout_seconds" yaml:"embedding_timeout_seconds"`
	EmbeddingPath                         string `mapstructure:"embedding_path" yaml:"embedding_path"`
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
		viper.SetDefault("ai.embedding_path", "/embed")
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
			if path == "" {
				log.Fatalf("failed to read config file: %v", err)
			}
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
