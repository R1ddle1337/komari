package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/database/accounts"
)

const maxSensitiveRequestBytes = 1 << 20

func RequireSensitive2FA() gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := VerifySensitive2FA(c); err != nil {
			status := http.StatusUnauthorized
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				status = http.StatusRequestEntityTooLarge
			}
			RespondError(c, status, err.Error())
			c.Abort()
			return
		}
		// 标记本请求已通过敏感操作校验，避免下游（RPC 边界）重复校验。
		c.Set("sensitive_2fa_verified", true)
		c.Next()
	}
}

// VerifySensitive2FACore 传输无关的 2FA 校验核心。
// 输入原始值:userUUID、2FA code、是否为 API Key。
// API Key 豁免;未启用 2FA 的用户放行;其余需要有效 code。
func VerifySensitive2FACore(userUUID, code string, isAPIKey bool) error {
	return verifySensitive2FA(userUUID, isAPIKey, func() (string, error) { return code, nil })
}

// Resolve the code only after confirming this account requires a factor.
// Upload streams must remain untouched when 2FA is disabled or exempted.
func verifySensitive2FA(userUUID string, isAPIKey bool, readCode func() (string, error)) error {
	if isAPIKey {
		return nil
	}
	if userUUID == "" {
		return err2FARequired()
	}
	user, err := accounts.GetUserByUUID(userUUID)
	if err != nil {
		return err
	}
	if user.TwoFactor == "" {
		return nil
	}
	code, err := readCode()
	if err != nil {
		return err
	}
	if code == "" {
		return err2FARequired()
	}
	valid, err := accounts.Verify2Fa(userUUID, code)
	if err != nil {
		return err
	}
	if !valid {
		return err2FAInvalid()
	}
	return nil
}

// VerifySensitive2FA gin 适配层:从 gin.Context 提取参数后委托核心校验。
func VerifySensitive2FA(c *gin.Context) error {
	_, isAPIKey := c.Get("api_key")
	uuidRaw, _ := c.Get("uuid")
	uuid, _ := uuidRaw.(string)
	return verifySensitive2FA(uuid, isAPIKey, func() (string, error) { return get2FACode(c) })
}

func get2FACode(c *gin.Context) (string, error) {
	if code, ok := c.Get("2fa_code"); ok {
		if codeString, ok := code.(string); ok && codeString != "" {
			return codeString, nil
		}
	}
	if code := c.GetHeader("X-2FA-Code"); code != "" {
		return code, nil
	}
	if code := c.GetHeader("X-Two-Factor-Code"); code != "" {
		return code, nil
	}
	for _, key := range []string{"2fa_code", "two_factor_code", "otp"} {
		if code := c.Query(key); code != "" {
			return code, nil
		}
	}
	if c.Request.Body == nil || c.Request.Method == http.MethodGet {
		return "", nil
	}
	// Binary/multipart uploads carry their factor in a header or query. Do not
	// consume or cap an upload while trying to interpret it as a JSON object.
	mediaType, _, err := mime.ParseMediaType(c.GetHeader("Content-Type"))
	if err != nil || (mediaType != "application/json" &&
		!(strings.HasPrefix(mediaType, "application/") && strings.HasSuffix(mediaType, "+json"))) {
		return "", nil
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSensitiveRequestBytes)
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return "", err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	if len(bodyBytes) == 0 {
		return "", nil
	}
	var body map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &body); err != nil {
		return "", nil
	}
	for _, key := range []string{"2fa_code", "two_factor_code", "otp"} {
		if value, ok := body[key].(string); ok && value != "" {
			return value, nil
		}
	}
	return "", nil
}

func err2FARequired() error {
	return &sensitive2FAError{"2FA code is required"}
}

func err2FAInvalid() error {
	return &sensitive2FAError{"Invalid 2FA code"}
}

type sensitive2FAError struct {
	message string
}

func (e *sensitive2FAError) Error() string {
	return e.message
}
