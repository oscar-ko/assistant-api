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

	return &profile{
		TeamID:      strings.TrimSpace(userInfo.TeamID),
		UserID:      strings.TrimSpace(userInfo.UserID),
		DisplayName: strings.TrimSpace(userInfo.DisplayName),
		Email:       strings.TrimSpace(userInfo.Email),
		Picture:     strings.TrimSpace(userInfo.Picture),
	}, nil
}
