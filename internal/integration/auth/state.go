package auth

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"github.com/gin-gonic/gin"
)

// 此檔放在 integration/auth 的原因：
// 1. OAuth state 屬於跨 provider（LINE/Google/...）可重用的共用能力。
// 2. 不應放在單一 provider 目錄，避免未來新增 provider 時重複實作。
//
// 目前實作屬於「Web OAuth adapter」：
// - 直接依賴 gin.Context。
// - 使用 cookie 保存/清除 state。
//
// 若未來要支援 App 或純後端流程，可再拆分為：
// - 核心層：純 Go 的 state 產生/驗證介面。
// - 傳輸層：cookie/header/DB/Redis 等不同儲存與傳遞實作。
/** provider/line/routes.go 中的 oauthStart() 會使用這個函式產生 state 並寫入 cookie，然後導向 LINE 授權頁。
	authorizeURL := "https://access.line.me/oauth2/v2.1/authorize?" + url.Values{
		"response_type": {"code"},
		"client_id":     {strings.TrimSpace(bot.ChannelID)},
		"redirect_uri":  {redirectURI},
		"state":         {state}, <-------- 這裡就是 OAuth state，用來防止 CSRF 攻擊
		"scope":         {strings.TrimSpace(config.Line.Scopes)},
	}.Encode()
**/
// GenerateState 產生 OAuth state 用的隨機字串。
func GenerateState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SetStateCookie 寫入一次性 state cookie，供 callback 做 CSRF 驗證。
// 此函式是 web 導向實作，依賴瀏覽器 cookie 行為。
func SetStateCookie(c *gin.Context, name, value string, maxAgeSeconds int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   maxAgeSeconds,
		HttpOnly: true,
		Secure:   c.Request.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

// GetStateCookie 讀取 state cookie，若不存在回傳空字串。
// 由於使用 cookie，主要適用在瀏覽器導向 OAuth callback。
func GetStateCookie(c *gin.Context, name string) string {
	v, _ := c.Cookie(name)
	return v
}

// ClearStateCookie 清除 callback 用完的 state cookie。
// callback 成功或失敗後都應清除，避免重放或污染下一次流程。
func ClearStateCookie(c *gin.Context, name string) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   c.Request.TLS != nil,
		SameSite: http.SameSiteLaxMode,
	})
}

// ValidateState 檢查 state 是否有效；若 cookie 遺失則保持向下相容。
// 注意：這是為了兼容部分內嵌瀏覽器/跨站情境；高安全需求可改成嚴格比對。
func ValidateState(got, expected string) bool {
	if got == "" {
		return false
	}
	if expected == "" {
		return true
	}
	return got == expected
}
