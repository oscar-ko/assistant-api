package line

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	aiembedding "assistant-api/internal/integration/ai/embedding"
	aillminteraction "assistant-api/internal/integration/ai/llm_interaction"
	aireranker "assistant-api/internal/integration/ai/reranker"
	"assistant-api/internal/integration/auth"
	"assistant-api/internal/repository"
	"assistant-api/internal/usecase/ai/topkfilter"

	"github.com/gin-gonic/gin"
)

const stateCookieName = "line_oauth_state"

// RegisterRoutes 註冊 LINE OAuth 與綁定路由。
func RegisterRoutes(r gin.IRouter, client *ent.Client) {
	// 基礎 repository：處理 LINE 綁定與訊息持久化。
	lineRepo := repository.NewLineRepo(client)
	channelMessageRepo := repository.NewChannelMessageRepo(client)
	// action route repository 提供 top-k 向量召回查詢能力。
	actionRouteRepo := repository.NewActionRouteRepo(client)
	// embedding client 是第一階段召回必備依賴。
	embeddingClient := aiembedding.NewClient(
		config.AI.Embedding.URL,
		config.AI.Embedding.TimeoutSeconds,
		config.AI.Embedding.Path,
		config.AI.Embedding.MaxAttempts,
		config.AI.Embedding.RetryBackoffMS,
		config.AI.Embedding.AliveProbeIntervalMS,
		config.AI.Embedding.AliveProbeTimeoutMS,
		config.AI.Embedding.AliveSuccessTTLMS,
		config.AI.Embedding.AliveFailureCooldownMS,
	)
	filterService := topkfilter.NewService(actionRouteRepo, embeddingClient, "zh-TW", config.AI.Embedding.RetrievalTopK)
	if config.AI.Reranker.Enabled {
		// 第二階段 cross-encoder 精排 client，僅對 retrieval 候選做重排。
		// 流程是先 retrieval(top-k) 再 rerank，不會讓 reranker 直接查全量路由，
		// 以控制延遲與成本，同時提升最終排序精準度。
		rerankerClient := aireranker.NewClient(
			config.AI.Reranker.URL,
			config.AI.Reranker.TimeoutSeconds,
			config.AI.Reranker.Path,
			config.AI.Reranker.MaxAttempts,
			config.AI.Reranker.RetryBackoffMS,
			config.AI.Reranker.AliveProbeIntervalMS,
			config.AI.Reranker.AliveProbeTimeoutMS,
			config.AI.Reranker.AliveSuccessTTLMS,
			config.AI.Reranker.AliveFailureCooldownMS,
		)
		filterService = topkfilter.NewServiceWithReranker(actionRouteRepo, embeddingClient, rerankerClient, "zh-TW", config.AI.Embedding.RetrievalTopK, config.AI.Reranker.TopK)
	}
	// 第三階段：LLM 互動服務，把 rerank 後的候選交給模型選出最終唯一一個 action。
	llmInteractionService := aillminteraction.NewInteractionService(aillminteraction.NewInteractionClient(
		config.AI.LLMInteraction.ServiceURL,
		config.AI.LLMInteraction.ServiceTimeoutSeconds,
	))
	followUpSender, _ := NewPushMessageService()

	// OAuth 相關端點。
	r.GET("/line/bind", bindPage)
	r.GET("/line/oauth/start", oauthStart)
	r.GET("/line/oauth/callback", oauthCallback(lineRepo))
	// Webhook 採 handler -> service 分層，便於後續替換 queue/worker 實作。
	r.POST("/line/webhook", webhookHandler(NewWebhookServiceWithOptions(channelMessageRepo, WebhookServiceOptions{LLMInteraction: llmInteractionService, TopKFilter: filterService, FollowUpSender: followUpSender})))
}

// bindPage 回傳 LINE 綁定頁面。
func bindPage(c *gin.Context) {
	c.File("static/line-bind.html")
}

// oauthStart 啟動 LINE OAuth 流程並導向授權頁。
func oauthStart(c *gin.Context) {
	// OAuth 基本設定缺失時直接拒絕，避免導向後才失敗。
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

	// 若未明確配置 redirect_uri，動態按目前請求協議與 host 推導。
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

	// 導向 LINE 授權頁，讓使用者完成 consent。
	c.Redirect(http.StatusFound, authorizeURL)
}

// oauthCallback 處理 LINE OAuth callback，完成綁定或建立使用者。
func oauthCallback(repo lineBindRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		// 先驗證 state，阻擋 CSRF。
		state := c.Query("state")
		expectedState := auth.GetStateCookie(c, stateCookieName)
		if !auth.ValidateState(state, expectedState) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid oauth state"})
			return
		}
		auth.ClearStateCookie(c, stateCookieName)

		// 取得授權碼，供後續換 token + 拉 profile。
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

		// 若有設定外部 bot URL，綁定完成後直接導頁。
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
