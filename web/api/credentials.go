package api

import (
	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/pkg/rpc"
)

// NewCredentialValidator snapshots a connection's original credentials and
// identity. Each invocation checks current credential validity without falling
// back to another identity. The closure never retains the pooled gin.Context,
// so it is also safe for terminal forwarding after its handler has returned.
func NewCredentialValidator(c *gin.Context) func() bool {
	p := GetPrincipal(c)
	if p == nil {
		p = IdentifyPrincipal(c)
	}
	switch p.Type {
	case rpc.PrincipalAPIKey:
		key := c.GetHeader("Authorization")
		return func() bool { return isApiKeyValid(key) }
	case rpc.PrincipalUser:
		session, _ := c.Cookie("session_token")
		expectedUUID := p.UserUUID
		return func() bool {
			uuid, err := accounts.GetSession(session)
			return err == nil && uuid != "" && uuid == expectedUUID
		}
	case rpc.PrincipalAgent:
		token, expectedUUID := extractClientToken(c), p.ClientUUID
		return func() bool {
			uuid, err := checkTokenAndGetUUID(token)
			return err == nil && uuid != "" && uuid == expectedUUID
		}
	case rpc.PrincipalAnonymous:
		return func() bool { return true }
	default:
		return func() bool { return false }
	}
}
