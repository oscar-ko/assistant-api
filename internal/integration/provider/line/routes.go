package line

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/integration/auth"
	"assistant-api/internal/integration/provider/messageintent"
	"assistant-api/internal/repository"

	"github.com/gin-gonic/gin"
)

const stateCookieName = "line_oauth_state"

// RegisterRoutes 註冊 LINE OAuth 與綁定路由。
func RegisterRoutes(r gin.IRouter, client *ent.Client) {
	lineRepo := repository.NewLineRepo(client)
	channelMessageRepo := repository.NewChannelMessageRepo(client)
	messageIntentClassifier := messageintent.NewClassifier(config.AI.MessageIntentClassifierURL, config.AI.MessageIntentClassifierTimeoutSeconds)

	r.GET("/line/bind", bindPage)
	r.GET("/line/oauth/start", oauthStart)
	r.GET("/line/oauth/callback", oauthCallback(lineRepo))
	// Webhook 採 handler -> service 分層，便於後續替換 queue/worker 實作。
	r.POST("/line/webhook", webhookHandler(NewWebhookService(channelMessageRepo, messageIntentClassifier)))
}

// bindPage 回傳 LINE 綁定頁面。
func bindPage(c *gin.Context) {
	c.File("static/line-bind.html")
}

// oauthStart 啟動 LINE OAuth 流程並導向授權頁。
func oauthStart(c *gin.Context) {
	if strings.TrimSpace(config.Line.ChannelID) == "" || strings.TrimSpace(config.Line.ClientSecret) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "line oauth config is incomplete"})
		return
	}

	state, err := auth.GenerateState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate oauth state"})
		return
	}
	auth.SetStateCookie(c, stateCookieName, state, 600)

	redirectURI := strings.TrimSpace(config.Line.RedirectURI)
	if redirectURI == "" {
		scheme := "http"
		if c.Request.TLS != nil {
			scheme = "https"
		}
		redirectURI = fmt.Sprintf("%s://%s/line/oauth/callback", scheme, c.Request.Host)
	}

	authorizeURL := "https://access.line.me/oauth2/v2.1/authorize?" + url.Values{
		"response_type": {"code"},
		"client_id":     {config.Line.ChannelID},
		"redirect_uri":  {redirectURI},
		"state":         {state},
		"scope":         {strings.TrimSpace(config.Line.Scopes)},
	}.Encode()

	c.Redirect(http.StatusFound, authorizeURL)
}

// oauthCallback 處理 LINE OAuth callback，完成綁定或建立使用者。
func oauthCallback(repo lineBindRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		state := c.Query("state")
		expectedState := auth.GetStateCookie(c, stateCookieName)
		if !auth.ValidateState(state, expectedState) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid oauth state"})
			return
		}
		auth.ClearStateCookie(c, stateCookieName)

		code := c.Query("code")
		if code == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing oauth code"})
			return
		}

		profile, err := getProfileByAuthCode(code)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		u, err := bindUser(c.Request.Context(), repo, profile)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		if strings.TrimSpace(config.Line.AssistantBotURL) != "" {
			c.Redirect(http.StatusFound, config.Line.AssistantBotURL)
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"status":       "bound",
			"line_user_id": profile.UserID,
			"user": gin.H{
				"id":    u.ID,
				"name":  u.Name,
				"email": u.Email,
			},
		})
	}
}
