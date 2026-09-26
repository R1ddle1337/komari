package admin

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/accounts"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/pquerna/otp/totp"
)

func TestMain(m *testing.M) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:komari_admin_test?mode=memory&cache=shared"
	dbcore.GetDBInstance()
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func TestTwoFactorSetupCannotOverwriteEnabledFactor(t *testing.T) {
	u, err := accounts.CreateAccount("two-factor-http-test", "test-only-password")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { accounts.DeleteAccountByUsername(u.Username) })
	const original = "JBSWY3DPEHPK3PXP"
	const replacement = "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ"
	if err := accounts.Enable2Fa(u.UUID, original); err != nil {
		t.Fatal(err)
	}
	code, err := totp.GenerateCode(replacement, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("uuid", u.UUID) })
	r.GET("/generate", Generate2FA)
	r.POST("/enable", Enable2FA)
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		path := "/generate"
		if method == http.MethodPost {
			path = "/enable?code=" + code
		}
		q := httptest.NewRequest(method, path, nil)
		q.AddCookie(&http.Cookie{Name: "2fa_secret", Value: replacement})
		w := httptest.NewRecorder()
		r.ServeHTTP(w, q)
		if w.Code != http.StatusConflict {
			t.Fatalf("%s status=%d", method, w.Code)
		}
	}
	got, err := accounts.GetUserByUUID(u.UUID)
	if err != nil || got.TwoFactor != original {
		t.Fatalf("existing factor was changed: %v", err)
	}
}

func TestTwoFactorCookieSecurity(t *testing.T) {
	for _, scheme := range []string{"http", "https"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, scheme+"://example.test/generate", nil)
		setTwoFactorCookie(c, "test-secret", 1800)
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode || cookies[0].Secure != (scheme == "https") {
			t.Fatalf("unsafe %s cookie", scheme)
		}
	}
}
