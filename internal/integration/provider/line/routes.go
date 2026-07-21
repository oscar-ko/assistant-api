package line

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	aillminteraction "assistant-api/internal/integration/ai/llm_interaction"
	aitopkfilter "assistant-api/internal/integration/ai/topkfilter"
	"assistant-api/internal/integration/auth"
	"assistant-api/internal/repository"

	"assistant-api/internal/integration/provider/oauthredirect"

	"github.com/gin-gonic/gin"
)

const stateCookieName = "line_oauth_state"

// RegisterRoutes 註冊 LINE OAuth 與綁定路由。
//
// 角色分工：
// - /line/oauth/*：處理 LINE 使用者綁定授權
// - /line/webhook：處理入站事件與共用 AI 流程
//
// 注意：private channel 會在綁定 callback 內建立，
// 入站持久化流程不再負責補建 channel。
func RegisterRoutes(r gin.IRouter, client *ent.Client) {
	// 基礎 repository：處理 LINE 綁定與訊息持久化。
	lineRepo := repository.NewLineRepo(client)
	channelMessageRepo := repository.NewChannelMessageRepo(client)
	// action route repository 提供 top-k 向量召回查詢能力。
	actionRouteRepo := repository.NewActionRouteRepo(client)
	filterService, err := aitopkfilter.BuildServiceFromConfig(actionRouteRepo, config.AI)
	if err != nil {
		panic(fmt.Errorf("failed to initialize top-k filter service: %w", err))
	}
	llmInteractionService, err := aillminteraction.BuildServiceFromConfig(config.AI, config.LLMProviders)
	if err != nil {
		panic(fmt.Errorf("failed to initialize llm interaction service: %w", err))
	}
	// 第三階段：LLM 互動服務，把 rerank 後的候選交給模型選出最終唯一一個 action。
	defaultBot, err := config.Line.DefaultBot()
	if err != nil {
		panic(fmt.Errorf("failed to initialize default line bot: %w", err))
	}
	defaultFollowUpSender, err := NewPushMessageServiceForBot(defaultBot)
	if err != nil {
		panic(fmt.Errorf("failed to initialize default line push service: %w", err))
	}

	// OAuth 相關端點。
	r.GET("/line/bind", bindPage)
	r.GET("/line/oauth/start", oauthStart)
	r.GET("/line/oauth/start/:bot_key", oauthStart)
	r.GET("/line/oauth/callback", oauthCallback(lineRepo, channelMessageRepo))
	r.GET("/line/oauth/callback/:bot_key", oauthCallback(lineRepo, channelMessageRepo))
	// Webhook 採 handler -> service 分層，便於後續替換 queue/worker 實作。
	// /line/webhook：不帶 bot_key 的舊路徑，固定使用設定檔第一筆 bot（default bot），確保既有整合不受影響。
	r.POST("/line/webhook", webhookHandler(NewWebhookServiceWithOptions(channelMessageRepo, WebhookServiceOptions{LLMInteraction: llmInteractionService, TopKFilter: filterService, FollowUpSender: defaultFollowUpSender, Bot: defaultBot})))
	// /line/webhook/:bot_key：多 bot 專用路徑，依 bot_key 動態解析出對應的 LINE bot 設定，
	// 並為該次請求建立專屬的 push service 與 webhook service，避免多個 bot 共用同一份 token/user id。
	r.POST("/line/webhook/:bot_key", func(c *gin.Context) {
		bot, err := config.Line.BotByKey(c.Param("bot_key"))
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
		followUpSender, err := NewPushMessageServiceForBot(bot)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		webhookHandler(NewWebhookServiceWithOptions(channelMessageRepo, WebhookServiceOptions{LLMInteraction: llmInteractionService, TopKFilter: filterService, FollowUpSender: followUpSender, Bot: bot}))(c)
	})
}

// bindPage 回傳 LINE 綁定頁面。
func bindPage(c *gin.Context) {
	c.File("static/line-bind.html")
}

// oauthStart 啟動 LINE OAuth 流程並導向授權頁。
func oauthStart(c *gin.Context) {
	bot, err := lineBotFromParam(c)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	// OAuth 基本設定缺失時直接拒絕，避免導向後才失敗。
	if strings.TrimSpace(bot.ChannelID) == "" || strings.TrimSpace(bot.ChannelSecret) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "line oauth config is incomplete"})
		return
	}

	state, err := auth.GenerateState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate oauth state"})
		return
	}
	auth.SetStateCookie(c, stateCookieName, state, 600)

	redirectURI := oauthredirect.Resolve(c.Request, config.Line.RedirectURI, lineOAuthCallbackPath(bot))

	authorizeURL := "https://access.line.me/oauth2/v2.1/authorize?" + url.Values{
		"response_type": {"code"},
		"client_id":     {strings.TrimSpace(bot.ChannelID)},
		"redirect_uri":  {redirectURI},
		"state":         {state},
		"scope":         {strings.TrimSpace(config.Line.Scopes)},
	}.Encode()

	// 導向 LINE 授權頁，讓使用者完成 consent。
	c.Redirect(http.StatusFound, authorizeURL)
}

// oauthCallback 處理 LINE OAuth callback，完成綁定或建立使用者。
func oauthCallback(repo lineBindRepository, channelRepo *repository.ChannelMessageRepo) gin.HandlerFunc {
	return func(c *gin.Context) {
		bot, err := lineBotFromParam(c)
		if err != nil {
			c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
			return
		}
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

		profile, err := getProfileByAuthCode(code, bot)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		u, err := bindUser(c.Request.Context(), repo, profile)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		// 嚴格模式：LINE 綁定成功後立即建立 private channel。
		// 之後入站訊息只允許寫入既有 channel，不在入站流程補建。
		if channelRepo == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "channel repository not initialized"})
			return
		}
		lineUserID := strings.TrimSpace(profile.UserID)
		if lineUserID == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "line user id is empty"})
			return
		}
		privateChannelName := strings.TrimSpace(u.Name)
		if privateChannelName == "" {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "bound user name is empty"})
			return
		}
		// 以 line user id 作為 private channel 的 group_id。
		// 這個決策可讓後續所有 LINE 私訊入站都穩定映射到同一筆 channel。
		if _, err := channelRepo.GetOrCreateChannel(c.Request.Context(), "line", lineUserID, "private", privateChannelName); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to create line private channel"})
			return
		}

		// 若有設定外部 bot URL，綁定完成後直接導頁。
		if strings.TrimSpace(bot.AssistantBotURL) != "" {
			c.Redirect(http.StatusFound, bot.AssistantBotURL)
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

// lineBotFromParam 依 gin route 上的 :bot_key 參數解析出對應的 LINE bot 設定。
// 若路由沒有 bot_key（例如 /line/oauth/start 舊路徑），BotByKey 會 fallback 成 DefaultBot。
func lineBotFromParam(c *gin.Context) (config.LineBotConfig, error) {
	if c == nil {
		return config.LineBotConfig{}, fmt.Errorf("gin context is nil")
	}
	return config.Line.BotByKey(c.Param("bot_key"))
}

// lineOAuthCallbackPath 依 bot 設定產生對應的 OAuth callback 路徑。
//
// default bot（或未命名 key）沿用舊的 /line/oauth/callback，維持既有整合相容；
// 其餘具名 bot 則導向 /line/oauth/callback/:bot_key，讓 LINE 換 token 後能導回正確 bot 的處理流程。
func lineOAuthCallbackPath(bot config.LineBotConfig) string {
	key := strings.TrimSpace(bot.Key)
	if key == "" || strings.EqualFold(key, "default") {
		return "/line/oauth/callback"
	}
	return "/line/oauth/callback/" + url.PathEscape(key)
}
