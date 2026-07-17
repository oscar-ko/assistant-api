package oauthredirect

import (
	"fmt"
	"net/http"
	"strings"
)

// Resolve 會優先使用已設定的完整 redirect URI。
//
// 如果設定檔沒有填值，就根據目前 request 的協定與 Host 動態組出完整網址，
// 讓 LINE / Slack 的 OAuth callback 可以共用同一套推導邏輯，只保留各自的 path。
func Resolve(request *http.Request, configuredURI string, callbackPath string) string {
	configuredURI = strings.TrimSpace(configuredURI)
	if configuredURI != "" {
		return configuredURI
	}
	if request == nil {
		return ""
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return fmt.Sprintf("%s://%s%s", scheme, strings.TrimSpace(request.Host), callbackPath)
}
