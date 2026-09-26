package admin

import (
	"errors"
	"image/png"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/web/api"
	"github.com/pquerna/otp/totp"
)

func setTwoFactorCookie(c *gin.Context, secret string, maxAge int) {
	utils.SetAuthCookie(c, "2fa_secret", secret, maxAge, http.SameSiteStrictMode)
}

func Generate2FA(c *gin.Context) {
	user, err := accounts.GetUserByUUID(c.GetString("uuid"))
	if err != nil {
		api.RespondError(c, http.StatusUnauthorized, "User not found")
		return
	}
	if user.TwoFactor != "" {
		api.RespondError(c, http.StatusConflict, accounts.ErrTwoFactorAlreadyEnabled.Error())
		return
	}
	secret, img, err := accounts.Generate2Fa()
	if err != nil {
		api.RespondError(c, 500, "Failed to generate 2FA: "+err.Error())
		return
	}
	setTwoFactorCookie(c, secret, 1800)
	c.Header("Content-Type", "image/png")
	c.Writer.WriteHeader(200)
	png.Encode(c.Writer, img)
}

func Enable2FA(c *gin.Context) {
	uuid, _ := c.Get("uuid")
	secret, _ := c.Cookie("2fa_secret")
	code := c.Query("code")
	if secret == "" || uuid == nil || code == "" {
		api.RespondError(c, 400, "2FA secret or code not provided")
		return
	}
	if !totp.Validate(code, secret) {
		api.RespondError(c, 400, "Invalid 2FA code")
		return
	}
	err := accounts.Enable2Fa(uuid.(string), secret)
	if err != nil {
		if errors.Is(err, accounts.ErrTwoFactorAlreadyEnabled) {
			api.RespondError(c, http.StatusConflict, err.Error())
			return
		}
		api.RespondError(c, 500, "Failed to enable 2FA: "+err.Error())
		return
	}
	setTwoFactorCookie(c, "", -1)

	api.RespondSuccess(c, "2FA enabled successfully")
}

func Disable2FA(c *gin.Context) {
	uuid, _ := c.Get("uuid")
	err := accounts.Disable2Fa(uuid.(string))
	if err != nil {
		api.RespondError(c, 500, "Failed to disable 2FA: "+err.Error())
		return
	}
	api.RespondSuccess(c, "")
}
