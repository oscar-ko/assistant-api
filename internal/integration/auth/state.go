package auth

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"

	"github.com/gin-gonic/gin"
)

// GenerateState 產生 OAuth state 用的隨機字串。
func GenerateState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// SetStateCookie 寫入一次性 state cookie，供 callback 做 CSRF 驗證。
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
func GetStateCookie(c *gin.Context, name string) string {
	v, _ := c.Cookie(name)
	return v
}

// ClearStateCookie 清除 callback 用完的 state cookie。
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
func ValidateState(got, expected string) bool {
	if got == "" {
		return false
	}
	if expected == "" {
		return true
	}
	return got == expected
}
