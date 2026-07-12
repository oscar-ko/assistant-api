package app

import (
	"context"
	"crypto/rand"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"assistant-api/internal/config"
	"assistant-api/internal/ent"
	"assistant-api/internal/ent/line"
	"assistant-api/internal/ent/user"

	"github.com/gin-gonic/gin"
)

//go:embed web/line-bind.html
var lineBindHTML string

func registerLineBindRoutes(r gin.IRouter, client *ent.Client) {
	r.GET("/line/bind", lineBindPage)
	r.GET("/line/oauth/start", lineOAuthStart)
	r.GET("/line/oauth/callback", lineOAuthCallback(client))
}

func lineBindPage(c *gin.Context) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, lineBindHTML)
}

func lineOAuthStart(c *gin.Context) {
	if strings.TrimSpace(config.Line.ChannelID) == "" || strings.TrimSpace(config.Line.ClientSecret) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "line oauth config is incomplete"})
		return
	}

	state, err := randomState()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to generate oauth state"})
		return
	}

	c.SetCookie("line_oauth_state", state, 600, "/", "", false, true)

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

func lineOAuthCallback(client *ent.Client) gin.HandlerFunc {
	return func(c *gin.Context) {
		state := c.Query("state")
		expectedState, _ := c.Cookie("line_oauth_state")
		if state == "" || expectedState == "" || state != expectedState {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid oauth state"})
			return
		}

		code := c.Query("code")
		if code == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing oauth code"})
			return
		}

		profile, err := getLineProfileByAuthCode(code)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		u, err := bindLineUser(c.Request.Context(), client, profile)
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

type lineOAuthTokenResponse struct {
	IDToken string `json:"id_token"`
}

type lineProfile struct {
	UserID      string `json:"sub"`
	DisplayName string `json:"name"`
	Email       string `json:"email"`
	Picture     string `json:"picture"`
}

func getLineProfileByAuthCode(code string) (*lineProfile, error) {
	redirectURI := strings.TrimSpace(config.Line.RedirectURI)
	if redirectURI == "" {
		return nil, fmt.Errorf("line redirect_uri is empty")
	}

	resp, err := http.PostForm("https://api.line.me/oauth2/v2.1/token", url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {config.Line.ChannelID},
		"client_secret": {config.Line.ClientSecret},
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("line token exchange failed: %s", string(b))
	}

	var token lineOAuthTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, err
	}
	if strings.TrimSpace(token.IDToken) == "" {
		return nil, fmt.Errorf("line token response missing id_token")
	}

	return parseLineIDToken(token.IDToken)
}

func parseLineIDToken(idToken string) (*lineProfile, error) {
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

	var profile lineProfile
	if err := json.Unmarshal(decoded, &profile); err != nil {
		return nil, err
	}
	if strings.TrimSpace(profile.UserID) == "" {
		return nil, fmt.Errorf("line profile missing user id")
	}

	return &profile, nil
}

func bindLineUser(ctx context.Context, client *ent.Client, profile *lineProfile) (*ent.User, error) {
	lineID := strings.TrimSpace(profile.UserID)
	email := strings.TrimSpace(profile.Email)
	name := strings.TrimSpace(profile.DisplayName)
	picture := strings.TrimSpace(profile.Picture)
	if name == "" {
		name = "LINE User"
	}

	u, err := client.User.Query().Where(user.HasLineWith(line.LineUserIDEQ(lineID))).Only(ctx)
	if err == nil {
		return u, nil
	}
	if err != nil && !ent.IsNotFound(err) {
		return nil, err
	}

	if email != "" {
		uByEmail, e := client.User.Query().Where(user.EmailEQ(email)).Only(ctx)
		if e == nil {
			hasLine, he := client.Line.Query().Where(line.HasUserWith(user.IDEQ(uByEmail.ID))).Exist(ctx)
			if he != nil {
				return nil, he
			}
			if hasLine {
				return nil, fmt.Errorf("user already bound to another line account")
			}

			if _, ce := client.Line.Create().
				SetLineUserID(lineID).
				SetDisplayName(name).
				SetNillableEmail(nullable(email)).
				SetNillablePicture(nullable(picture)).
				SetUser(uByEmail).
				Save(ctx); ce != nil {
				return nil, ce
			}
			return uByEmail, nil
		}
		if e != nil && !ent.IsNotFound(e) {
			return nil, e
		}
	} else {
		email = fmt.Sprintf("line_%s@line.local", lineID)
	}

	uNew, err := client.User.Create().SetName(name).SetEmail(email).Save(ctx)
	if err != nil {
		return nil, err
	}

	if _, err := client.Line.Create().
		SetLineUserID(lineID).
		SetDisplayName(name).
		SetNillableEmail(nullable(strings.TrimSpace(profile.Email))).
		SetNillablePicture(nullable(picture)).
		SetUser(uNew).
		Save(ctx); err != nil {
		return nil, err
	}

	return uNew, nil
}

func nullable(v string) *string {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil
	}
	return &v
}

func randomState() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
