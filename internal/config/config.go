package config

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync"

	"github.com/spf13/viper"
)

const defaultConfigPath = "configs/app.yml"

type configuration struct {
	RunMode  *string         `mapstructure:"run_mode"`
	Log      *LogConfig      `mapstructure:"log"`
	Server   *ServerConfig   `mapstructure:"server"`
	Database *DatabaseConfig `mapstructure:"database"`
	GraphQL  *GraphQLConfig  `mapstructure:"graphql"`
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
	SQLiteDSN string `mapstructure:"sqlite_dsn" yaml:"sqlite_dsn"`
}

type GraphQLConfig struct {
	QueryPath      string `mapstructure:"query_path" yaml:"query_path"`
	PlaygroundPath string `mapstructure:"playground_path" yaml:"playground_path"`
}

var (
	once sync.Once

	RunMode  string
	Log      LogConfig
	Server   ServerConfig
	Database DatabaseConfig
	GraphQL  GraphQLConfig

	config = &configuration{
		RunMode:  &RunMode,
		Log:      &Log,
		Server:   &Server,
		Database: &Database,
		GraphQL:  &GraphQL,
	}
)

func MustLoad() {
	once.Do(func() {
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
		viper.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

		viper.SetDefault("run_mode", "dev")
		viper.SetDefault("log.level", "info")
		viper.SetDefault("log.filename", "")
		viper.SetDefault("log.max_size", 10)
		viper.SetDefault("log.max_age", 7)
		viper.SetDefault("log.max_backups", 5)
		viper.SetDefault("server.port", "8080")
		viper.SetDefault("database.sqlite_dsn", "file:ent.db?_fk=1")
		viper.SetDefault("graphql.query_path", "/query")
		viper.SetDefault("graphql.playground_path", "/playground")

		if err := viper.ReadInConfig(); err != nil {
			if path == "" {
				log.Fatalf("failed to read config file: %v", err)
			}
			log.Fatalf("failed to read config file %q: %v", path, err)
		}

		requiredKeys := []string{
			"server.port",
			"database.sqlite_dsn",
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
	})
}
