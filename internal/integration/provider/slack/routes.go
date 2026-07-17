package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	aillminteraction "assistant-api/internal/integration/ai/llm_interaction"
	aitopkfilter "assistant-api/internal/integration/ai/topkfilter"
	"assistant-api/internal/integration/auth"
	"assistant-api/internal/repository"

	"github.com/gin-gonic/gin"
)

const (
	// stateCookieName 用於 Slack 安裝 OAuth（bot install）流程的 CSRF state cookie。
	stateCookieName = "slack_oauth_state"
	// loginStateCookieName 用於 Slack Login（OpenID 綁定）流程的 CSRF state cookie。
	loginStateCookieName = "slack_login_oauth_state"
)

// RegisterRoutes 註冊 Slack provider 的 HTTP 路由。
//
// 路由分兩條主線：
// 1) /slack/oauth/*：Slack App 安裝授權
// 2) /slack/login/*：Slack OpenID 綁定登入
//
// 安裝與登入分離的原因：
// - 安裝流程的主體是 workspace（bot token / app scope）
// - 登入流程的主體是 user（OpenID 身分）
// - 分離後可避免 callback 混用造成 state 或資料語意錯亂
func RegisterRoutes(r gin.IRouter, client *ent.Client) {
	slackRepo := repository.NewSlackRepo(client)
	channelMessageRepo := repository.NewChannelMessageRepo(client)
	actionRouteRepo := repository.NewActionRouteRepo(client)

	filterService, err := aitopkfilter.BuildServiceFromConfig(actionRouteRepo, config.AI)
	if err != nil {
		panic(fmt.Errorf("failed to initialize top-k filter service: %w", err))
	}
	llmInteractionService, err := aillminteraction.BuildServiceFromConfig(config.AI, config.LLMProviders)
	if err != nil {
		panic(fmt.Errorf("failed to initialize llm interaction service: %w", err))
	}
	followUpSender, _ := NewPushMessageService(slackRepo)

	r.GET("/slack/oauth/start", oauthStart)
	r.GET("/slack/oauth/callback", oauthCallback(slackRepo))
	r.GET("/slack/login/start", loginStart)
	r.GET("/slack/login/callback", loginCallback(slackRepo, channelMessageRepo))
	r.POST("/slack/events", webhookHandler(NewWebhookServiceWithOptions(channelMessageRepo, slackRepo, WebhookServiceOptions{
		LLMInteraction: llmInteractionService,
		TopKFilter:     filterService,
		FollowUpSender: followUpSender,
	})))
}

// oauthStart 啟動 Slack App 安裝 OAuth。
//
// 嚴格模式：必要設定缺值直接報錯，不做 fallback。
// 這裡只做導轉，不寫 DB；資料落地集中在 callback 成功後處理。
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
	if redirectToConfiguredOAuthHost(c, redirectURI) {
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
// 與 oauthStart 的差異：
// - login 用 login_redirect_uri / login_scopes
// - callback 會進入使用者綁定與 private channel 初始化
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
	if redirectToConfiguredOAuthHost(c, loginRedirectURI) {
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

func redirectToConfiguredOAuthHost(c *gin.Context, configuredURI string) bool {
	if c == nil {
		return false
	}
	if c.Query("public") == "1" {
		return false
	}
	parsed, err := url.Parse(strings.TrimSpace(configuredURI))
	if err != nil {
		return false
	}
	targetHost := strings.TrimSpace(parsed.Host)
	targetScheme := strings.TrimSpace(parsed.Scheme)
	if targetHost == "" || targetScheme == "" {
		return false
	}
	currentHost := strings.TrimSpace(c.Request.Host)
	if strings.EqualFold(currentHost, targetHost) {
		return false
	}
	values := c.Request.URL.Query()
	values.Set("public", "1")
	publicURL := url.URL{
		Scheme:   targetScheme,
		Host:     targetHost,
		Path:     c.Request.URL.Path,
		RawQuery: values.Encode(),
	}
	c.Redirect(http.StatusFound, publicURL.String())
	return true
}

func slackOAuthStartURL() string {
	redirectURI := strings.TrimSpace(config.Slack.RedirectURI)
	parsed, err := url.Parse(redirectURI)
	if err != nil || strings.TrimSpace(parsed.Scheme) == "" || strings.TrimSpace(parsed.Host) == "" {
		return "/slack/oauth/start"
	}
	return (&url.URL{
		Scheme:   parsed.Scheme,
		Host:     parsed.Host,
		Path:     "/slack/oauth/start",
		RawQuery: "public=1",
	}).String()
}

// slackLoginStartURL 產生 Slack login/start 的導向網址。
//
// 這個 helper 與 slackOAuthStartURL 對稱，讓 install 完成後可以直接接續進入 OpenID 綁定流程。
func slackLoginStartURL() string {
	loginRedirectURI := strings.TrimSpace(config.Slack.LoginRedirectURI)
	parsed, err := url.Parse(loginRedirectURI)
	if err != nil || strings.TrimSpace(parsed.Scheme) == "" || strings.TrimSpace(parsed.Host) == "" {
		return "/slack/login/start"
	}
	return (&url.URL{
		Scheme:   parsed.Scheme,
		Host:     parsed.Host,
		Path:     "/slack/login/start",
		RawQuery: "public=1",
	}).String()
}

// oauthCallback 處理 Slack App 安裝 OAuth callback。
//
// 流程設計：
// 1) 先完成 workspace install，取得 bot token 與 team id
// 2) install 成功後直接導向 login/start，讓使用者接著做 OpenID 綁定
// 3) login 的 state / callback 與 install 完全分開，避免混用
func oauthCallback(repo *repository.SlackRepo) gin.HandlerFunc {
	return func(c *gin.Context) {
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

		teamID := strings.TrimSpace(payload.Team.ID)
		botToken := strings.TrimSpace(payload.AccessToken)
		if teamID == "" {
			c.JSON(http.StatusBadGateway, gin.H{"error": "slack oauth response missing team id"})
			return
		}
		if botToken == "" {
			c.JSON(http.StatusBadGateway, gin.H{"error": "slack oauth response missing access_token"})
			return
		}
		if repo == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "slack repository not initialized"})
			return
		}
		if err := repo.UpsertWorkspaceInstall(c.Request.Context(), teamID, strings.TrimSpace(payload.Team.Name), botToken, strings.TrimSpace(payload.BotUserID)); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to persist slack workspace install"})
			return
		}

		// 安裝成功後直接進入 login/start，讓使用者接續完成綁定流程。
		// 這裡只做導向，不把 install 與 login 的語意混在同一個 callback 回應中。
		c.Redirect(http.StatusFound, slackLoginStartURL())
	}
}

// loginCallback 處理 Slack Login callback，並在綁定成功後建立 private channel。
//
// 嚴格順序：
// 1) 驗證 state/code
// 2) 交換 OpenID profile
// 3) 綁定本地 user
// 4) 開 DM conversation 取得 channel id
// 5) 建立本地 private channel
// 任何一步失敗都直接回錯，不做隱式降級。
func loginCallback(repo slackBindRepository, channelRepo *repository.ChannelMessageRepo) gin.HandlerFunc {
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
		privateChannelName := strings.TrimSpace(u.Name)
		if privateChannelName == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "bound user name is empty"})
			return
		}

		// 嚴格模式：綁定成功後先開 DM，並以 DM channel id 建立 private channel。
		if channelRepo == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "channel repository not initialized"})
			return
		}
		dmChannelID, err := OpenDMChannelID(c.Request.Context(), repo, strings.TrimSpace(profile.TeamID), strings.TrimSpace(profile.UserID))
		if err != nil {
			if errors.Is(err, repository.ErrSlackWorkspaceInstallNotFound) {
				c.JSON(http.StatusConflict, gin.H{
					"error":       "slack workspace install required",
					"team_id":     strings.TrimSpace(profile.TeamID),
					"install_url": slackOAuthStartURL(),
					"next_action": "install_slack_app",
					"detail":      err.Error(),
				})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if _, err := channelRepo.GetOrCreateChannel(c.Request.Context(), "slack", dmChannelID, "private", privateChannelName); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create slack private channel"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status":           "bound",
			"slack_team_id":    strings.TrimSpace(profile.TeamID),
			"slack_user_id":    strings.TrimSpace(profile.UserID),
			"slack_dm_channel": dmChannelID,
			"user": gin.H{
				"id":    u.ID,
				"name":  u.Name,
				"email": u.Email,
			},
		})
	}
}

// maskToken 將敏感 token 做遮罩後回傳。
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
