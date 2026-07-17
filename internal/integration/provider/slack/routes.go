package slack

import (
	"encoding/json"
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

const stateCookieName = "slack_oauth_state"

func RegisterRoutes(r gin.IRouter, client *ent.Client) {
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
	followUpSender, _ := NewPushMessageService()

	r.GET("/slack/oauth/start", oauthStart)
	r.GET("/slack/oauth/callback", oauthCallback)
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

	state, err := auth.GenerateState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate oauth state"})
		return
	}
	auth.SetStateCookie(c, stateCookieName, state, 600)

	redirectURI := strings.TrimSpace(config.Slack.RedirectURI)
	if redirectURI == "" {
		scheme := "http"
		if c.Request.TLS != nil {
			scheme = "https"
		}
		redirectURI = fmt.Sprintf("%s://%s/slack/oauth/callback", scheme, c.Request.Host)
	}

	values := url.Values{}
	values.Set("client_id", strings.TrimSpace(config.Slack.ClientID))
	values.Set("scope", strings.TrimSpace(config.Slack.Scopes))
	if strings.TrimSpace(config.Slack.UserScopes) != "" {
		values.Set("user_scope", strings.TrimSpace(config.Slack.UserScopes))
	}
	values.Set("redirect_uri", redirectURI)
	values.Set("state", state)

	authorizeURL := "https://slack.com/oauth/v2/authorize?" + values.Encode()
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
		scheme := "http"
		if c.Request.TLS != nil {
			scheme = "https"
		}
		redirectURI = fmt.Sprintf("%s://%s/slack/oauth/callback", scheme, c.Request.Host)
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
