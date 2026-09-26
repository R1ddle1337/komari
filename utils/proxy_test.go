package utils

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestForwardedIdentityRequiresAnExplicitProxy(t *testing.T) {
	t.Setenv("KOMARI_TRUSTED_PROXIES", "127.0.0.1,172.22.0.1")
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	if err := ConfigureTrustedProxies(engine); err != nil {
		t.Fatal(err)
	}
	engine.GET("/", func(c *gin.Context) { c.String(200, c.ClientIP()+" "+GetScheme(c)) })
	for _, test := range []struct{ remote, expected string }{
		{"203.0.113.5:1234", "203.0.113.5 http"},
		{"172.22.0.1:1234", "198.51.100.7 https"},
		{"172.22.0.9:1234", "172.22.0.9 http"},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://panel.example/", nil)
		request.RemoteAddr = test.remote
		request.Header.Set("X-Forwarded-For", "192.0.2.123, 198.51.100.7")
		request.Header.Set("X-Forwarded-Proto", "https")
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		if response.Body.String() != test.expected {
			t.Fatalf("remote %s: got %q, want %q", test.remote, response.Body.String(), test.expected)
		}
	}
}

func TestProxyConfigurationFailsClosed(t *testing.T) {
	for _, value := range []string{"*", "0.0.0.0/0", "::/0", "not-an-address"} {
		t.Setenv("KOMARI_TRUSTED_PROXIES", value)
		if err := ConfigureTrustedProxies(gin.New()); err == nil {
			t.Fatalf("accepted unsafe proxy configuration %q", value)
		}
	}
	t.Setenv("KOMARI_TRUSTED_PROXIES", "")
	if isTrustedProxy("127.0.0.1:80") {
		t.Fatal("explicit empty proxy list still trusts loopback")
	}
}

func TestAuthCookiesFollowTrustedSchemeAndStayHTTPOnly(t *testing.T) {
	t.Setenv("KOMARI_TRUSTED_PROXIES", "172.22.0.1")
	for _, name := range []string{"session_token", "oauth_state", "binding_external_account", "temp_key", "2fa_secret"} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "http://panel.example/", nil)
		c.Request.RemoteAddr = "172.22.0.1:1234"
		c.Request.Header.Set("X-Forwarded-Proto", "https")
		SetAuthCookie(c, name, "test-value", 60, http.SameSiteLaxMode)
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
			t.Fatalf("cookie protection missing for %s", name)
		}
	}
}
