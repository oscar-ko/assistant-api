package slack

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"assistant-api/internal/config"
)

type oauthTokenResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
	AccessToken string `json:"access_token"`
}

// oauthUserInfoResponse 對應 Slack OpenID userInfo API 回傳欄位。
//
// 注意：team_id 與 user_id 在 Slack OIDC 回應中不是一般短欄位名稱，
// 而是使用帶命名空間的 claim key，因此 json tag 需完整對應。
type oauthUserInfoResponse struct {
	OK          bool   `json:"ok"`
	Error       string `json:"error"`
	TeamID      string `json:"https://slack.com/team_id"`
	UserID      string `json:"https://slack.com/user_id"`
	DisplayName string `json:"name"`
	Email       string `json:"email"`
	Picture     string `json:"picture"`
}

type profile struct {
	TeamID      string
	UserID      string
	DisplayName string
	Email       string
	Picture     string
}

// getProfileByAuthCode 以授權碼交換 token，並向 Slack userInfo 端點取得使用者資料。
//
// 流程分兩段：
// 1) openid.connect.token：用授權碼取得 access token。
// 2) openid.connect.userInfo：用 access token 換取可綁定身份資料。
//
// 這裡採嚴格模式：
// - redirectURI 必須明確配置（不在此處補值）
// - token/userinfo 任一步驟異常都直接回錯，不做靜默降級
func getProfileByAuthCode(code string, redirectURI string) (*profile, error) {
	redirectURI = strings.TrimSpace(redirectURI)
	if redirectURI == "" {
		return nil, fmt.Errorf("slack login redirect uri is empty")
	}

	resp, err := http.PostForm("https://slack.com/api/openid.connect.token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {strings.TrimSpace(code)},
		"redirect_uri":  {redirectURI},
		"client_id":     {strings.TrimSpace(config.Slack.ClientID)},
		"client_secret": {strings.TrimSpace(config.Slack.ClientSecret)},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("slack token exchange failed: %s", string(b))
	}

	var token oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, err
	}
	if !token.OK {
		message := strings.TrimSpace(token.Error)
		if message == "" {
			message = "slack token exchange failed"
		}
		return nil, fmt.Errorf("%s", message)
	}

	accessToken := strings.TrimSpace(token.AccessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("slack token response missing access_token")
	}

	req, err := http.NewRequest(http.MethodGet, "https://slack.com/api/openid.connect.userInfo", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	userInfoResp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, err
	}
	defer userInfoResp.Body.Close()

	if userInfoResp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(userInfoResp.Body)
		return nil, fmt.Errorf("slack userinfo failed: %s", string(b))
	}

	var userInfo oauthUserInfoResponse
	if err := json.NewDecoder(userInfoResp.Body).Decode(&userInfo); err != nil {
		return nil, err
	}
	if !userInfo.OK {
		message := strings.TrimSpace(userInfo.Error)
		if message == "" {
			message = "slack userinfo failed"
		}
		return nil, fmt.Errorf("%s", message)
	}

	// 回傳前統一 trim，避免上游綁定時因前後空白造成查詢 miss。
	return &profile{
		TeamID:      strings.TrimSpace(userInfo.TeamID),
		UserID:      strings.TrimSpace(userInfo.UserID),
		DisplayName: strings.TrimSpace(userInfo.DisplayName),
		Email:       strings.TrimSpace(userInfo.Email),
		Picture:     strings.TrimSpace(userInfo.Picture),
	}, nil
}
