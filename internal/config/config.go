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
	RunMode      *string                       `mapstructure:"run_mode"`
	Log          *LogConfig                    `mapstructure:"log"`
	Server       *ServerConfig                 `mapstructure:"server"`
	Database     *DatabaseConfig               `mapstructure:"database"`
	PostgreSQL   *PostgreSQLConfig             `mapstructure:"postgresql"`
	AI           *AIConfig                     `mapstructure:"ai"`
	LLMProviders *map[string]LLMProviderConfig `mapstructure:"llm_providers"`
	Line         *LineConfig                   `mapstructure:"line"`
	Slack        *SlackConfig                  `mapstructure:"slack"`
	GraphQL      *GraphQLConfig                `mapstructure:"graphql"`
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

// AIConfig 集中管理 AI 相關子系統設定，依用途拆成多個子區塊：
// - LLMInteraction：依角色路由到指定 provider/model（問答、追問、決策）
// - Embedding：第一階段候選召回（recall）
// - Reranker：第二階段候選精排（precision）
// - TodoReminder：待辦提醒專用的時間解析、近端上下文與 evidence 視窗控制
type AIConfig struct {
	LLMInteraction LLMInteractionConfig `mapstructure:"llm_interaction" yaml:"llm_interaction"`
	Embedding      EmbeddingConfig      `mapstructure:"embedding" yaml:"embedding"`
	Reranker       RerankerConfig       `mapstructure:"reranker" yaml:"reranker"`
	Classifier     ClassifierConfig     `mapstructure:"classifier" yaml:"classifier"`
	TodoReminder   TodoReminderConfig   `mapstructure:"todo_reminder" yaml:"todo_reminder"`
}

// TodoReminderConfig controls Todo Reminder-specific runtime behavior.
type TodoReminderConfig struct {
	// Timezone 是 due_text 正規化時使用的 IANA timezone，例如 Asia/Taipei。
	// 它屬於 Todo Reminder 行為設定，和訊息召回窗口同放在 todo_reminder 區塊，讓待辦相關調參集中管理。
	Timezone string `mapstructure:"timezone" yaml:"timezone"`
	// ReplyChainMaxDepth 控制顯式 reply/quote 往上追溯幾層 parent message。
	// 每一層 parent 都會形成自己的 message window；設太大會讓 prompt 快速膨脹，因此必須由設定檔明確指定。
	ReplyChainMaxDepth int `mapstructure:"reply_chain_max_depth" yaml:"reply_chain_max_depth"`
	// RecentContextMessageLimit 控制 Todo Reminder 在 implicit analysis 前，最多往前抓幾則同 channel 的最近訊息。
	// 這是「原始近端召回窗口」：它只決定 recent window 的候選大小，最後送進 analyzer 的總量仍由 MaxContextMessages 截斷。
	RecentContextMessageLimit int `mapstructure:"recent_context_message_limit" yaml:"recent_context_message_limit"`
	// EvidenceAnchorLimitPerCandidate 控制每個 candidate 最多取幾則活躍 evidence anchor 來重建上下文。
	EvidenceAnchorLimitPerCandidate int `mapstructure:"evidence_anchor_limit_per_candidate" yaml:"evidence_anchor_limit_per_candidate"`
	// EvidenceWindowBeforeLimit 控制每個 evidence anchor 往前取幾則同 channel 訊息。
	EvidenceWindowBeforeLimit int `mapstructure:"evidence_window_before_limit" yaml:"evidence_window_before_limit"`
	// EvidenceWindowAfterLimit 控制每個 evidence anchor 往後取幾則同 channel 訊息。
	EvidenceWindowAfterLimit int `mapstructure:"evidence_window_after_limit" yaml:"evidence_window_after_limit"`
	// MaxCandidateContexts 控制每次 implicit analysis 最多提供幾個 active candidate context 給 analyzer。
	MaxCandidateContexts int `mapstructure:"max_candidate_contexts" yaml:"max_candidate_contexts"`
	// MaxContextMessages 控制 evidence 小窗和 recent window 合併後，最多提供幾則 context messages 給 analyzer。
	MaxContextMessages int `mapstructure:"max_context_messages" yaml:"max_context_messages"`
}

// ClassifierConfig controls the local message classifier used by realtime handlers.
type ClassifierConfig struct {
	Enabled bool     `mapstructure:"enabled" yaml:"enabled"`
	Target  string   `mapstructure:"target" yaml:"target"`
	Labels  []string `mapstructure:"labels" yaml:"labels"`
}

// LLMInteractionConfig 為角色導向的 LLM 互動設定。
type LLMInteractionConfig struct {
	// Decision 指定「最終 action 決策」使用的 target。
	Decision LLMRoleConfig `mapstructure:"decision" yaml:"decision"`
	// Chat 指定「一般問答/追問」使用的 target。
	Chat LLMRoleConfig `mapstructure:"chat" yaml:"chat"`
	// Translate 指定翻譯流程使用的 target。
	Translate LLMRoleConfig `mapstructure:"translate" yaml:"translate"`
	// ContextAnalyzer 指定「短文本 + 近端上下文」分析使用的 target。
	// 它和 decision/chat/translate 一樣走 llm_providers profile；差別在於用途不是完整對話決策，
	// 而是給 realtime 服務在 bounded context 內做輕量判斷，例如 todo 補欄位、calendar 補資訊、follow-up 判斷等。
	// 這讓同一個 LLM interaction 服務可以依 profile 使用不同模型，而不把模型選型寫死在業務流程中。
	ContextAnalyzer LLMRoleConfig `mapstructure:"context_analyzer" yaml:"context_analyzer"`
	ChatGPT         ChatGPTConfig `mapstructure:"chatgpt" yaml:"chatgpt"`
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

// LLMRoleConfig 描述一個 role 對應到的 target。
type LLMRoleConfig struct {
	Profile     string   `mapstructure:"profile" yaml:"profile"`
	Timeout     int      `mapstructure:"timeout" yaml:"timeout"`
	MaxToken    *int     `mapstructure:"max_token" yaml:"max_token"`
	Temperature *float64 `mapstructure:"temperature" yaml:"temperature"`
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
	Target         string `mapstructure:"target" yaml:"target"`
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
	Target         string `mapstructure:"target" yaml:"target"`
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

// LineConfig 為 LINE 平台共用資訊，讓多個 LINE Bot 可以共用同一組 OAuth 導向設定。
//
// 設計理由：一個 LINE Login channel 底下可以掛多個 Messaging API bot；
// redirect_uri、scopes 屬於 OAuth 導向流程共用資訊，因此獨立於 bots 清單之外，
// 避免每新增一個 bot 就要重複填一次相同的 OAuth 設定。
type LineConfig struct {
	// RedirectURI 為 LINE OAuth 授權完成後導回的 URL；所有 bot 共用同一個導向端點。
	RedirectURI string `mapstructure:"redirect_uri" yaml:"redirect_uri"`
	// Scopes 為 LINE Login 授權範圍；所有 bot 共用同一組 scope 設定。
	Scopes string `mapstructure:"scopes" yaml:"scopes"`
	// Bots 為此服務實際掛載的 LINE bot 清單；每個 bot 各自持有自己的 channel 憑證。
	Bots []LineBotConfig `mapstructure:"bots" yaml:"bots"`
}

// LineBotConfig 為單一 LINE bot 的 Login（OAuth）與 Messaging API 憑證。
//
// 注意：channel_id / channel_secret 這兩個欄位「跟著 bot 走」，不是共用設定；
// 因為 LINE 後台每建立一個新的 Messaging API channel，就會拿到一組獨立的 channel_id / channel_secret，
// 混用會導致 OAuth 換 token 時打錯 channel。
type LineBotConfig struct {
	// Key 為此 bot 的識別代稱，用於 webhook / OAuth 路徑（例如 /line/webhook/:bot_key）與設定檔內部比對；未填時視為 "default"。
	Key string `mapstructure:"key" yaml:"key"`
	// ChannelID 為此 bot 專屬的 LINE Login channel ID，OAuth 換 token 時作為 client_id 使用。
	ChannelID string `mapstructure:"channel_id" yaml:"channel_id"`
	// ChannelSecret 為此 bot 專屬的 LINE Login channel secret，OAuth 換 token 時作為 client_secret 使用。
	ChannelSecret string `mapstructure:"channel_secret" yaml:"channel_secret"`
	// ChannelToken 為此 bot 的 Messaging API channel access token，用於 push/reply 訊息與建立 webhook client。
	ChannelToken string `mapstructure:"channel_token" yaml:"channel_token"`
	// BotUserID 為此 bot 在 LINE 平台上的 User ID（U 開頭），用於判斷群組訊息是否 mention 到 bot 本身。
	BotUserID string `mapstructure:"bot_user_id" yaml:"bot_user_id"`
	// AssistantBotURL 為此 bot 的加好友連結；OAuth 綁定完成後若有設定，會導頁到此網址。
	AssistantBotURL string `mapstructure:"assistant_bot_url" yaml:"assistant_bot_url"`
}

// DefaultBot 回傳未指定 bot key 時使用的 LINE bot（清單中的第一筆）。
//
// 用途：現有呼叫端（例如舊版 /line/webhook、/line/oauth/start 未帶 bot_key 時）
// 需要一個明確的預設 bot，這裡固定採用設定檔 bots 清單的第一筆，
// 並在必要欄位缺漏時直接回錯，避免服務帶著不完整憑證啟動。
func (l LineConfig) DefaultBot() (LineBotConfig, error) {
	if len(l.Bots) == 0 {
		return LineBotConfig{}, fmt.Errorf("line bots is empty")
	}
	return normalizeLineBotConfig(l.Bots[0])
}

// BotByKey 依 key 尋找對應的 LINE bot。
//
// key 為空字串時等同呼叫 DefaultBot()，讓既有「不帶 bot_key」的路由可以延續舊行為；
// 比對採用不分大小寫，避免設定檔與 URL 路徑大小寫不一致造成誤判找不到 bot。
func (l LineConfig) BotByKey(key string) (LineBotConfig, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return l.DefaultBot()
	}
	for _, item := range l.Bots {
		bot := item
		if strings.TrimSpace(bot.Key) == "" {
			bot.Key = "default"
		}
		if strings.EqualFold(strings.TrimSpace(bot.Key), key) {
			return normalizeLineBotConfig(bot)
		}
	}
	return LineBotConfig{}, fmt.Errorf("unknown line bot key: %s", key)
}

// normalizeLineBotConfig 補上預設 key，並驗證此 bot 是否具備啟動所需的必要憑證。
//
// 採 fail-fast 原則：channel_id / channel_secret / channel_token / bot_user_id
// 任一欄位缺漏，都直接回錯，不做任何 fallback，避免服務用不完整設定啟動後才在執行期間出錯。
func normalizeLineBotConfig(bot LineBotConfig) (LineBotConfig, error) {
	if strings.TrimSpace(bot.Key) == "" {
		bot.Key = "default"
	}
	if strings.TrimSpace(bot.ChannelID) == "" {
		return LineBotConfig{}, fmt.Errorf("line bot %q channel_id is empty", bot.Key)
	}
	if strings.TrimSpace(bot.ChannelSecret) == "" {
		return LineBotConfig{}, fmt.Errorf("line bot %q channel_secret is empty", bot.Key)
	}
	if strings.TrimSpace(bot.ChannelToken) == "" {
		return LineBotConfig{}, fmt.Errorf("line bot %q channel_token is empty", bot.Key)
	}
	if strings.TrimSpace(bot.BotUserID) == "" {
		return LineBotConfig{}, fmt.Errorf("line bot %q bot_user_id is empty", bot.Key)
	}
	return bot, nil
}

// SlackConfig 為 Slack 平台共用資訊，讓多個 Slack App / Bot 共用同一組導向端點與授權範圍。
type SlackConfig struct {
	RedirectURI      string           `mapstructure:"redirect_uri" yaml:"redirect_uri"`
	LoginRedirectURI string           `mapstructure:"login_redirect_uri" yaml:"login_redirect_uri"`
	Scopes           string           `mapstructure:"scopes" yaml:"scopes"`
	LoginScopes      string           `mapstructure:"login_scopes" yaml:"login_scopes"`
	UserScopes       string           `mapstructure:"user_scopes" yaml:"user_scopes"`
	Bots             []SlackBotConfig `mapstructure:"bots" yaml:"bots"`
}

// SlackBotConfig 為單一 Slack App / Bot 的 OAuth 與 Events API 憑證。
//
// 設計重點：
// - AppID 使用 Slack 官方 app_id，而不是自訂 bot key，讓 route、workspace install 與 webhook 驗章都能對齊 Slack 後台。
// - Name 是系統內部顯示用的人名，例如 Jarvis / Thor；之後 outbound message 或管理介面可用它呈現較好讀的 bot 名稱。
// - SigningSecret 用於 Events API request signature；多 bot 情境下會逐一比對，找出本次 webhook 屬於哪個 Slack App。
// - Slack bot_user_id 不屬於靜態設定；它是 workspace install 結果，必須從 slack_workspaces 依 app_id + team_id 讀取。
type SlackBotConfig struct {
	Name              string `mapstructure:"name" yaml:"name"`
	AppID             string `mapstructure:"app_id" yaml:"app_id"`
	ClientID          string `mapstructure:"client_id" yaml:"client_id"`
	ClientSecret      string `mapstructure:"client_secret" yaml:"client_secret"`
	SigningSecret     string `mapstructure:"signing_secret" yaml:"signing_secret"`
	VerificationToken string `mapstructure:"verification_token" yaml:"verification_token"`
}

// DefaultBot 回傳未指定 app id 時使用的 Slack bot（清單中的第一筆）。
func (s SlackConfig) DefaultBot() (SlackBotConfig, error) {
	if len(s.Bots) == 0 {
		return SlackBotConfig{}, fmt.Errorf("slack bots is empty")
	}
	return normalizeSlackBotConfig(s.Bots[0])
}

// BotByAppID 依 Slack app_id 尋找對應的 Slack bot。
//
// app_id 是 Slack App 的官方穩定識別，適合放在 OAuth callback route、manifest redirect URL 與 dev helper input。
// 這裡刻意不支援其他別名欄位，避免多 bot 設定中出現同一個 App 被多套 key 指到的歧義。
func (s SlackConfig) BotByAppID(appID string) (SlackBotConfig, error) {
	appID = strings.TrimSpace(appID)
	if appID == "" {
		return s.DefaultBot()
	}
	for _, item := range s.Bots {
		if strings.EqualFold(strings.TrimSpace(item.AppID), appID) {
			return normalizeSlackBotConfig(item)
		}
	}
	return SlackBotConfig{}, fmt.Errorf("unknown slack app_id: %s", appID)
}

// BotBySigningSecret 尋找可驗證目前 webhook 簽章的 Slack bot。
func (s SlackConfig) BotBySigningSecret(match func(string) bool) (SlackBotConfig, error) {
	if match == nil {
		return SlackBotConfig{}, fmt.Errorf("slack signing secret matcher is nil")
	}
	for _, item := range s.Bots {
		bot, err := normalizeSlackBotConfig(item)
		if err != nil {
			return SlackBotConfig{}, err
		}
		if match(bot.SigningSecret) {
			return bot, nil
		}
	}
	return SlackBotConfig{}, fmt.Errorf("no slack bot matched request signature")
}

func normalizeSlackBotConfig(bot SlackBotConfig) (SlackBotConfig, error) {
	if strings.TrimSpace(bot.Name) == "" {
		return SlackBotConfig{}, fmt.Errorf("slack bot name is empty")
	}
	if strings.TrimSpace(bot.AppID) == "" {
		return SlackBotConfig{}, fmt.Errorf("slack bot app_id is empty")
	}
	if strings.TrimSpace(bot.ClientID) == "" {
		return SlackBotConfig{}, fmt.Errorf("slack bot %q client_id is empty", bot.AppID)
	}
	if strings.TrimSpace(bot.ClientSecret) == "" {
		return SlackBotConfig{}, fmt.Errorf("slack bot %q client_secret is empty", bot.AppID)
	}
	if strings.TrimSpace(bot.SigningSecret) == "" {
		return SlackBotConfig{}, fmt.Errorf("slack bot %q signing_secret is empty", bot.AppID)
	}
	return bot, nil
}

// LLMProviderConfig 描述一個 provider 與其底下 profiles。
type LLMProviderConfig struct {
	Type     string                      `mapstructure:"type" yaml:"type"`
	URL      string                      `mapstructure:"url" yaml:"url"`
	Token    string                      `mapstructure:"token" yaml:"token"`
	Headers  map[string]string           `mapstructure:"headers" yaml:"headers"`
	Profiles map[string]LLMProfileConfig `mapstructure:"profiles" yaml:"profiles"`
}

// LLMProfileConfig 描述單一 provider profile 的連線資訊。
type LLMProfileConfig struct {
	URL            string   `mapstructure:"url" yaml:"url"`
	ModelName      string   `mapstructure:"model_name" yaml:"model_name"`
	TimeoutSeconds int      `mapstructure:"timeout_seconds" yaml:"timeout_seconds"`
	MaxTokens      *int     `mapstructure:"max_tokens" yaml:"max_tokens"`
	Temperature    *float64 `mapstructure:"temperature" yaml:"temperature"`
	// UseJSONResponseFmt 控制 OpenAI 類 chat completion 是否主動送出 response_format=json_object。
	// 針對 search-style model 這類不相容的 profile，應在設定檔關閉此開關，避免 API 直接回 400。
	UseJSONResponseFmt *bool  `mapstructure:"use_json_response_format" yaml:"use_json_response_format"`
	Path               string `mapstructure:"path" yaml:"path"`
	ActionDecisionPath string `mapstructure:"action_decision_path" yaml:"action_decision_path"`
	QuestionAnswerPath string `mapstructure:"question_answer_path" yaml:"question_answer_path"`
	// ContextAnalyzePath 是 9003 內部上下文分析入口。
	// 它刻意獨立於 question_answer，避免把系統流程判斷誤混成使用者可見的一般問答。
	ContextAnalyzePath string `mapstructure:"context_analyze_path" yaml:"context_analyze_path"`
	// TodoAnalyzePath 是 Todo Reminder 專用結構化分析入口。
	// 它和通用 context_analyze 分離，避免 todo candidate schema 演進時污染其他 realtime service。
	TodoAnalyzePath string `mapstructure:"todo_analyze_path" yaml:"todo_analyze_path"`
	// TodoDueTimePath 是 Todo Reminder 專用時間正規化入口。
	// 它只處理 due_text -> due_at，避免把時間解析規則塞回 todo_analyze 主契約。
	TodoDueTimePath string `mapstructure:"todo_due_time_path" yaml:"todo_due_time_path"`
	TranslatePath   string `mapstructure:"translate_path" yaml:"translate_path"`
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
	RunMode      string
	Log          LogConfig
	Server       ServerConfig
	Database     DatabaseConfig
	PostgreSQL   PostgreSQLConfig
	AI           AIConfig
	LLMProviders map[string]LLMProviderConfig
	Line         LineConfig
	Slack        SlackConfig
	GraphQL      GraphQLConfig

	config = &configuration{
		RunMode:      &RunMode,
		Log:          &Log,
		Server:       &Server,
		Database:     &Database,
		PostgreSQL:   &PostgreSQL,
		AI:           &AI,
		LLMProviders: &LLMProviders,
		Line:         &Line,
		Slack:        &Slack,
		GraphQL:      &GraphQL,
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
		// 將巢狀 key（如 line.bots.0.channel_id）對應為環境變數格式。
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
		viper.SetDefault("ai.llm_interaction.decision.profile", "local")
		viper.SetDefault("ai.llm_interaction.chat.profile", "local")
		viper.SetDefault("ai.llm_interaction.translate.profile", "local")
		// context_analyzer 是短文本上下文分析角色；預設仍沿用 local，實際部署應在 provider profile 指定 endpoint 與 model_name。
		viper.SetDefault("ai.llm_interaction.context_analyzer.profile", "local")
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
		// action decision JSON 格式錯誤重送次數。預設為 0，避免同一訊息被重送到 AI。
		viper.SetDefault("ai.llm_interaction.decision_json_retry_count", 0)
		// Todo Reminder 時間正規化預設使用台灣時區；部署到其他地區時應由 app.yml 明確覆寫。
		viper.SetDefault("ai.todo_reminder.timezone", "Asia/Taipei")
		// Todo Reminder 的近端 recent window：單位是往前幾則同 channel 訊息，不是時間長度。
		// 這個值只控制第一段相鄰對話召回；和 evidence 視窗合併後的 prompt 總量由 max_context_messages 控制。
		viper.SetDefault("ai.todo_reminder.recent_context_message_limit", 8)
		viper.SetDefault("ai.embedding.url", "http://127.0.0.1:9000")
		viper.SetDefault("ai.embedding.target", "aistant.embedding")
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
		viper.SetDefault("ai.reranker.target", "aistant.reranker")
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
		viper.SetDefault("line.redirect_uri", "")
		viper.SetDefault("line.scopes", "openid profile email")
		viper.SetDefault("line.bots", []LineBotConfig{})
		viper.SetDefault("slack.app_id", "")
		viper.SetDefault("slack.client_id", "")
		viper.SetDefault("slack.client_secret", "")
		viper.SetDefault("slack.signing_secret", "")
		viper.SetDefault("slack.verification_token", "")
		viper.SetDefault("slack.redirect_uri", "")
		viper.SetDefault("slack.login_redirect_uri", "")
		// Slack bot 被邀請進頻道時會用 conversations.info 解析頻道名稱。
		// history scope 只允許讀訊息，不允許讀頻道基本資訊；缺少 channels:read/groups:read 會讓 webhook lifecycle 在建立 channel 前因 missing_scope 失敗。
		viper.SetDefault("slack.scopes", "app_mentions:read,channels:history,channels:read,chat:write,groups:history,groups:read,im:history,mpim:history")
		viper.SetDefault("slack.login_scopes", "openid profile email")
		viper.SetDefault("slack.user_scopes", "")
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

// ResolveLocalProviderProfile 依 target 解析 provider/profile。
func ResolveLocalProviderProfile(providers map[string]LLMProviderConfig, target string) (LLMProviderConfig, LLMProfileConfig, error) {
	target = strings.TrimSpace(target)
	parts := strings.Split(target, ".")
	if len(parts) != 2 {
		return LLMProviderConfig{}, LLMProfileConfig{}, fmt.Errorf("target must be <provider>.<profile>")
	}
	providerKey := strings.TrimSpace(parts[0])
	profileKey := strings.TrimSpace(parts[1])
	if providerKey == "" || profileKey == "" {
		return LLMProviderConfig{}, LLMProfileConfig{}, fmt.Errorf("target must be <provider>.<profile>")
	}
	provider, ok := providers[providerKey]
	if !ok {
		return LLMProviderConfig{}, LLMProfileConfig{}, fmt.Errorf("unknown provider: %s", providerKey)
	}
	profile, ok := provider.Profiles[profileKey]
	if !ok {
		return LLMProviderConfig{}, LLMProfileConfig{}, fmt.Errorf("unknown profile %s for provider %s", profileKey, providerKey)
	}
	return provider, profile, nil
}
