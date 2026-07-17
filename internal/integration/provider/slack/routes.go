package slack

// stateCookieName 用於 Slack 安裝 OAuth（bot install）流程的 CSRF state cookie。

// loginStateCookieName 用於 Slack Login（OpenID 綁定）流程的 CSRF state cookie。
// 與安裝流程分離，避免兩條 OAuth 流程互相覆蓋 state。
import (
	"encoding/json"
	"fmt"
	// RegisterRoutes 註冊 Slack provider 的所有 HTTP 入口。
	//
	// 路由分成兩條主線：
	// 1) /slack/oauth/*：Slack App 安裝流程（目標是 bot 能力授權）
	// 2) /slack/login/*：Slack OpenID 登入綁定（目標是使用者身分綁定）
	//
	// 兩條線刻意分開，避免把「安裝 bot」與「登入綁定 user」混成同一個 callback。
	"net/http"
	"net/url"
	// ChannelMessageRepo 供 webhook 事件落庫與指令流程使用。
	"strings"
	// ActionRouteRepo + top-k filter 提供指令候選路由召回能力。
	"time"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	aillminteraction "assistant-api/internal/integration/ai/llm_interaction"
	// LLM 互動服務供 command flow 決策使用。
	aitopkfilter "assistant-api/internal/integration/ai/topkfilter"
	"assistant-api/internal/integration/auth"
	"assistant-api/internal/repository"

	// followUpSender 是 webhook 後續回覆能力；初始化失敗時由下游自行處理 nil sender。
	"github.com/gin-gonic/gin"
)

const (
	stateCookieName      = "slack_oauth_state"
	loginStateCookieName = "slack_login_oauth_state"
)

func RegisterRoutes(r gin.IRouter, client *ent.Client) {
	slackRepo := repository.NewSlackRepo(client)
	channelMessageRepo := repository.NewChannelMessageRepo(client)
	actionRouteRepo := repository.NewActionRouteRepo(client)
	filterService, err := aitopkfilter.BuildServiceFromConfig(actionRouteRepo, config.AI)
	// oauthStart 啟動 Slack App 安裝 OAuth。
	//
	// 嚴格模式（fail-fast）：
	// - client_id/client_secret 缺值直接回錯
	// - redirect_uri/scopes 缺值直接回錯
	// - 不做動態推導，不做預設補值
	if err != nil {
		panic(fmt.Errorf("failed to initialize top-k filter service: %w", err))
	}
	llmInteractionService, err := aillminteraction.BuildServiceFromConfig(config.AI, config.LLMProviders)
	if err != nil {
		panic(fmt.Errorf("failed to initialize llm interaction service: %w", err))
	}
	followUpSender, _ := NewPushMessageService()

	r.GET("/slack/oauth/start", oauthStart)
	r.GET("/slack/oauth/callback", oauthCallback)
	r.GET("/slack/login/start", loginStart)
	r.GET("/slack/login/callback", loginCallback(slackRepo))
	r.POST("/slack/events", webhookHandler(NewWebhookServiceWithOptions(channelMessageRepo, WebhookServiceOptions{
		LLMInteraction: llmInteractionService,
		TopKFilter:     filterService,
		FollowUpSender: followUpSender,
	})))
}

func oauthStart(c *gin.Context) {
	if strings.TrimSpace(config.Slack.ClientID) == "" || strings.TrimSpace(config.Slack.ClientSecret) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slack oauth config is incomplete"})
		return
	}
	redirectURI := strings.TrimSpace(config.Slack.RedirectURI)
	if redirectURI == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slack redirect_uri is empty"})
		return
	}
	scopes := strings.TrimSpace(config.Slack.Scopes)
	if scopes == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slack scopes is empty"})
		return
	}

	state, err := auth.GenerateState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate oauth state"})
		return
	}
	auth.SetStateCookie(c, stateCookieName, state, 600)

	values := url.Values{}
	values.Set("client_id", strings.TrimSpace(config.Slack.ClientID))
	values.Set("scope", scopes)
	if strings.TrimSpace(config.Slack.UserScopes) != "" {
		values.Set("user_scope", strings.TrimSpace(config.Slack.UserScopes))
	}
	values.Set("redirect_uri", redirectURI)
	values.Set("state", state)

	authorizeURL := "https://slack.com/oauth/v2/authorize?" + values.Encode()
	c.Redirect(http.StatusFound, authorizeURL)
}

// loginStart 啟動 Slack OpenID Connect 登入流程。
//
// 這條路徑專門用於「綁定使用者身份」，與 bot install 分離。
// 嚴格模式要求 login_redirect_uri 與 login_scopes 都必須明確設定。
func loginStart(c *gin.Context) {
	if strings.TrimSpace(config.Slack.ClientID) == "" || strings.TrimSpace(config.Slack.ClientSecret) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slack oauth config is incomplete"})
		return
	}
	loginRedirectURI := strings.TrimSpace(config.Slack.LoginRedirectURI)
	if loginRedirectURI == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slack login_redirect_uri is empty"})
		return
	}
	loginScopes := strings.TrimSpace(config.Slack.LoginScopes)
	if loginScopes == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slack login_scopes is empty"})
		return
	}

	state, err := auth.GenerateState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate oauth state"})
		return
	}
	auth.SetStateCookie(c, loginStateCookieName, state, 600)

	values := url.Values{}
	values.Set("response_type", "code")
	values.Set("client_id", strings.TrimSpace(config.Slack.ClientID))
	values.Set("scope", loginScopes)
	values.Set("redirect_uri", loginRedirectURI)
	values.Set("state", state)

	authorizeURL := "https://slack.com/openid/connect/authorize?" + values.Encode()
	c.Redirect(http.StatusFound, authorizeURL)
}

// slackOAuthAccessResponse 對應 Slack 安裝 OAuth 的 token exchange 回應。
// 這裡只保留目前流程會用到的欄位，避免結構過度膨脹。
type slackOAuthAccessResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
	AppID       string `json:"app_id"`
	AccessToken string `json:"access_token"`
	BotUserID   string `json:"bot_user_id"`
	Scope       string `json:"scope"`
	Team        struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"team"`
	AuthedUser struct {
		ID    string `json:"id"`
		Scope string `json:"scope"`
	} `json:"authed_user"`
}

// oauthCallback 處理 Slack App 安裝 OAuth callback。
//
// 流程：
// 1) 驗證 state 防止 CSRF
// 2) 檢查 code 與 redirect_uri
// 3) 呼叫 oauth.v2.access 交換 token
// 4) 回傳安裝結果摘要（不直接在此處寫入 config）
func oauthCallback(c *gin.Context) {
	state := c.Query("state")
	expectedState := auth.GetStateCookie(c, stateCookieName)
	if !auth.ValidateState(state, expectedState) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid oauth state"})
		return
	}
	auth.ClearStateCookie(c, stateCookieName)

	code := strings.TrimSpace(c.Query("code"))
	if code == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing oauth code"})
		return
	}
	redirectURI := strings.TrimSpace(config.Slack.RedirectURI)
	if redirectURI == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "slack redirect_uri is empty"})
		return
	}

	form := url.Values{}
	form.Set("client_id", strings.TrimSpace(config.Slack.ClientID))
	form.Set("client_secret", strings.TrimSpace(config.Slack.ClientSecret))
	form.Set("code", code)
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, "https://slack.com/api/oauth.v2.access", strings.NewReader(form.Encode()))
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to build slack oauth request"})
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "failed to call slack oauth"})
		return
	}
	defer resp.Body.Close()

	var payload slackOAuthAccessResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "invalid slack oauth response"})
		return
	}
	if !payload.OK {
		message := strings.TrimSpace(payload.Error)
		if message == "" {
			message = "slack oauth failed"
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": message})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":              "installed",
		"app_id":              strings.TrimSpace(payload.AppID),
		"team_id":             strings.TrimSpace(payload.Team.ID),
		"team_name":           strings.TrimSpace(payload.Team.Name),
		"bot_user_id":         strings.TrimSpace(payload.BotUserID),
		"bot_scope":           strings.TrimSpace(payload.Scope),
		"authed_user_id":      strings.TrimSpace(payload.AuthedUser.ID),
		"authed_user_scope":   strings.TrimSpace(payload.AuthedUser.Scope),
		"bot_token_preview":   maskToken(payload.AccessToken),
		"save_to_config_hint": "set slack.bot_token and slack.bot_user_id in app.local.yml",
	})
}

// loginCallback 處理 Slack Login callback，完成 Slack 身分與系統 user 綁定。
//
// 這裡只負責 OAuth 入口編排與錯誤回覆：
// - 取得 profile 與綁定策略由 getProfileByAuthCode / bindUser 負責
// - 路由層不承擔資料庫細節，維持職責單一
func loginCallback(repo slackBindRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		state := c.Query("state")
		expectedState := auth.GetStateCookie(c, loginStateCookieName)
		if !auth.ValidateState(state, expectedState) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid oauth state"})
			return
		}
		auth.ClearStateCookie(c, loginStateCookieName)

		code := strings.TrimSpace(c.Query("code"))
		if code == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing oauth code"})
			return
		}
		loginRedirectURI := strings.TrimSpace(config.Slack.LoginRedirectURI)
		if loginRedirectURI == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "slack login_redirect_uri is empty"})
			return
		}

		profile, err := getProfileByAuthCode(code, loginRedirectURI)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		u, err := bindUser(c.Request.Context(), repo, profile)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status":        "bound",
			"slack_team_id": strings.TrimSpace(profile.TeamID),
			"slack_user_id": strings.TrimSpace(profile.UserID),
			"user": gin.H{
				"id":    u.ID,
				"name":  u.Name,
				"email": u.Email,
			},
		})
	}
}

// maskToken 將敏感 token 做遮罩後回傳，避免完整 token 外洩到 API 回應。
func maskToken(raw string) string {
	v := strings.TrimSpace(raw)
	if v == "" {
		return ""
	}
	if len(v) <= 12 {
		return "******"
	}
	return v[:6] + "..." + v[len(v)-4:]
}
