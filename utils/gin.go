package utils

import (
	"github.com/gin-gonic/gin"
	"net/http"
)

func SetAuthCookie(c *gin.Context, name, value string, maxAge int, sameSite http.SameSite) {
	http.SetCookie(c.Writer, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: maxAge,
		Secure: GetScheme(c) == "https", HttpOnly: true, SameSite: sameSite})
}

// https://github.com/labstack/echo/blob/98ca08e7dd64075b858e758d6693bf9799340756/context.go#L275-L294
func GetScheme(c *gin.Context) string {
	// Can't use `r.Request.URL.Scheme`
	// See: https://groups.google.com/forum/#!topic/golang-nuts/pMUkBlQBDF0
	if c.Request.TLS != nil {
		return "https"
	}
	if isTrustedProxy(c.Request.RemoteAddr) {
		for _, header := range []string{"X-Forwarded-Proto", "X-Forwarded-Protocol", "X-Url-Scheme"} {
			if scheme := c.GetHeader(header); scheme == "http" || scheme == "https" {
				return scheme
			}
		}
		if c.GetHeader("X-Forwarded-Ssl") == "on" {
			return "https"
		}
	}
	return "http"
}

func GetCallbackURL(c *gin.Context) string {
	scheme := GetScheme(c)
	host := c.Request.Host
	return scheme + "://" + host + "/api/oauth_callback"
}
