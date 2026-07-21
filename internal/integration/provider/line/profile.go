package line

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"assistant-api/internal/config"
)

type oauthTokenResponse struct {
	IDToken string `json:"id_token"`
}

type profile struct {
	UserID      string `json:"sub"`
	DisplayName string `json:"name"`
	Email       string `json:"email"`
	Picture     string `json:"picture"`
}

// getProfileByAuthCode 以授權碼交換 token，並解析 LINE profile。
func getProfileByAuthCode(code string, bot config.LineBotConfig) (*profile, error) {
	redirectURI := strings.TrimSpace(config.Line.RedirectURI)
	if redirectURI == "" {
		return nil, fmt.Errorf("line redirect_uri is empty")
	}
	if strings.TrimSpace(bot.ChannelID) == "" || strings.TrimSpace(bot.ChannelSecret) == "" {
		return nil, fmt.Errorf("line oauth config is incomplete for bot %q", strings.TrimSpace(bot.Key))
	}

	resp, err := http.PostForm("https://api.line.me/oauth2/v2.1/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {strings.TrimSpace(bot.ChannelID)},
		"client_secret": {strings.TrimSpace(bot.ChannelSecret)},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("line token exchange failed: %s", string(b))
	}

	var token oauthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token.IDToken) == "" {
		return nil, fmt.Errorf("line token response missing id_token")
	}

	return parseIDToken(token.IDToken)
}

// parseIDToken 解析 id_token 的 JWT payload，取出使用者資訊。
func parseIDToken(idToken string) (*profile, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid id_token format")
	}

	payload := parts[1]
	decoded, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		padding := (4 - len(payload)%4) % 4
		if padding > 0 {
			payload += strings.Repeat("=", padding)
		}
		decoded, err = base64.URLEncoding.DecodeString(payload)
		if err != nil {
			return nil, err
		}
	}

	var p profile
	if err := json.Unmarshal(decoded, &p); err != nil {
		return nil, err
	}
	if strings.TrimSpace(p.UserID) == "" {
		return nil, fmt.Errorf("line profile missing user id")
	}

	return &p, nil
}
