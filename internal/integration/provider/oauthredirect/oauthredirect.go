package oauthredirect

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// Resolve 會優先使用已設定的完整 redirect URI。
//
// 如果設定檔沒有填值，就根據目前 request 的協定與 Host 動態組出完整網址，
// 讓 LINE / Slack 的 OAuth callback 可以共用同一套推導邏輯，只保留各自的 path。
func Resolve(request *http.Request, configuredURI string, callbackPath string) string {
	configuredURI = strings.TrimSpace(configuredURI)
	if configuredURI != "" {
		if strings.TrimSpace(callbackPath) != "" {
			// 多 Slack app / LINE bot 會共用同一個外部 host，但 callback path 可能因 app_id/bot_key 不同而改變；
			// 因此保留設定檔中的 scheme/host，僅覆寫 path，避免 OAuth callback 落到預設路徑造成 invalid_code。
			parsed, err := url.Parse(configuredURI)
			if err == nil && strings.TrimSpace(parsed.Scheme) != "" && strings.TrimSpace(parsed.Host) != "" {
				parsed.Path = callbackPath
				parsed.RawPath = ""
				parsed.RawQuery = ""
				parsed.Fragment = ""
				return parsed.String()
			}
		}
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
