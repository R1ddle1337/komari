package public

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/internal/config"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/web/api"
	"github.com/komari-monitor/komari/web/security"

	"github.com/gin-gonic/gin"
)

type LoginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
	TwoFa    string `json:"2fa_code"`
}

const sessionCookieMaxAge = 2592000

func setSessionCookie(c *gin.Context, value string, maxAge int) {
	utils.SetAuthCookie(c, "session_token", value, maxAge, http.SameSiteLaxMode)
}

func Login(c *gin.Context) {
	DisablePasswordLogin, _ := config.GetAs[bool](config.DisablePasswordLoginKey, false)
	if DisablePasswordLogin {
		api.RespondError(c, http.StatusForbidden, "Password login is disabled")
		return
	}
	c.Header("Cache-Control", "no-store")
	if !passwordLoginLimiter.allow(c.ClientIP(), time.Now()) {
		c.Header("Retry-After", "60")
		api.RespondError(c, http.StatusTooManyRequests, "Too many login attempts; retry later")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxLoginBytes)
	control := http.NewResponseController(c.Writer)
	_ = control.SetReadDeadline(time.Now().Add(security.LoginBodyTimeout))
	defer control.SetReadDeadline(time.Time{})

	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		var limit *http.MaxBytesError
		if errors.As(err, &limit) {
			api.RespondError(c, http.StatusRequestEntityTooLarge, "Login request is too large")
		} else {
			api.RespondError(c, http.StatusBadRequest, "Invalid request body")
		}
		return
	}
	var data LoginRequest
	err = json.Unmarshal(bodyBytes, &data)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if data.Username == "" || data.Password == "" {
		api.RespondError(c, http.StatusBadRequest, "Invalid request body: Username and password are required")
		return
	}

	// A slow body must not occupy a password-work slot. Only fully received,
	// valid credential requests compete for the bounded Argon2 work budget.
	select {
	case passwordLoginSlots <- struct{}{}:
		defer func() { <-passwordLoginSlots }()
	default:
		c.Header("Retry-After", "1")
		api.RespondError(c, http.StatusTooManyRequests, "Login is busy; retry later")
		return
	}
	uuid, passwordProof, success := accounts.CheckPasswordWithProof(data.Username, data.Password)
	if !success {
		api.RespondError(c, http.StatusUnauthorized, "Invalid credentials")
		return
	}
	// 2FA
	user, err := accounts.GetUserByUUID(uuid)
	if err != nil {
		api.RespondError(c, http.StatusUnauthorized, "Invalid credentials")
		return
	}
	if user.TwoFactor != "" { // 开启了2FA
		if data.TwoFa == "" {
			api.RespondError(c, http.StatusUnauthorized, "2FA code is required")
			return
		}
		if ok, err := accounts.Verify2Fa(uuid, data.TwoFa); err != nil || !ok {
			api.RespondError(c, http.StatusUnauthorized, "Invalid 2FA code")
			return
		}
	}
	// Create session
	session, err := accounts.CreatePasswordSession(uuid, passwordProof, sessionCookieMaxAge, c.Request.UserAgent(), c.ClientIP())
	if err != nil {
		if errors.Is(err, accounts.ErrCredentialsChanged) {
			api.RespondError(c, http.StatusUnauthorized, "Invalid credentials")
		} else {
			api.RespondError(c, http.StatusInternalServerError, "Failed to create session")
		}
		return
	}
	setSessionCookie(c, session, sessionCookieMaxAge)
	auditlog.Log(c.ClientIP(), uuid, "logged in (password)", "login")
	api.RespondSuccess(c, gin.H{"set-cookie": gin.H{"session_token": session}})
}
func Logout(c *gin.Context) {
	session, _ := c.Cookie("session_token")
	accounts.DeleteSession(session)
	setSessionCookie(c, "", -1)
	auditlog.Log(c.ClientIP(), "", "logged out", "logout")
	c.Redirect(302, "/")
}
