package api

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/cmd/flags"
	"github.com/komari-monitor/komari/database/dbcore"
	"github.com/komari-monitor/komari/database/models"
	"github.com/pquerna/otp/totp"
)

func TestMain(m *testing.M) {
	flags.DatabaseType = flags.DatabaseTypeSQLite
	flags.DatabaseFile = "file:komari_api_auth_test?mode=memory&cache=shared"
	dbcore.GetDBInstance()
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

type countingBody struct {
	remaining int
	read      int
}

func (b *countingBody) Read(p []byte) (int, error) {
	if b.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(p), b.remaining)
	for i := range p[:n] {
		p[i] = ' '
	}
	b.remaining -= n
	b.read += n
	return n, nil
}

func TestIdentityDoesNotReadUnauthenticatedBodies(t *testing.T) {
	for _, path := range []string{"/api/admin/upload/chunk", "/api/clients/v2/rpc", "/api/login", "/api/rpc2"} {
		t.Run(path, func(t *testing.T) {
			body := &countingBody{remaining: 20 << 20}
			r := gin.New()
			r.Use(IdentityMiddleware())
			r.POST(path, RequireRole(RoleAdmin), func(c *gin.Context) { t.Fatal("anonymous request was authorized") })
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, body))
			if w.Code != http.StatusUnauthorized || body.read != 0 {
				t.Fatalf("status=%d bytes read before authorization=%d", w.Code, body.read)
			}
		})
	}
}

func TestIdentityPreservesAgentStreamWithQueryToken(t *testing.T) {
	node := models.Client{UUID: "auth-stream", Token: "auth-stream-token", Name: "test"}
	if err := dbcore.GetDBInstance().Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dbcore.GetDBInstance().Delete(&node) })
	r := gin.New()
	r.Use(IdentityMiddleware())
	r.POST("/api/clients/transfer/id", RequireRole(RoleClient), func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		if err != nil || string(body) != "binary\x00stream" {
			t.Fatalf("stream changed during authentication: %q %v", body, err)
		}
		c.Status(http.StatusNoContent)
	})
	for _, queryKey := range []string{"token", "Authorization"} {
		w := httptest.NewRecorder()
		q := httptest.NewRequest(http.MethodPost, "/api/clients/transfer/id?"+queryKey+"="+node.Token, strings.NewReader("binary\x00stream"))
		r.ServeHTTP(w, q)
		if w.Code != http.StatusNoContent {
			t.Fatalf("query %s: status=%d", queryKey, w.Code)
		}
	}
}

func TestSensitiveCodeReadIsBoundedAndPreservesValidBody(t *testing.T) {
	body := &countingBody{remaining: 20 << 20}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/api/admin/task/exec", body)
	c.Request.Header.Set("Content-Type", "application/json")
	_, err := get2FACode(c)
	var tooLarge *http.MaxBytesError
	if !errors.As(err, &tooLarge) || body.read > maxSensitiveRequestBytes+1 {
		t.Fatalf("error=%v bytes read=%d", err, body.read)
	}
	const payload = `{"2fa_code":"123456","command":"echo test"}`
	c.Request = httptest.NewRequest(http.MethodPost, "/api/admin/task/exec", strings.NewReader(payload))
	c.Request.Header.Set("Content-Type", "application/json; charset=utf-8")
	code, err := get2FACode(c)
	restored, readErr := io.ReadAll(c.Request.Body)
	if err != nil || readErr != nil || code != "123456" || string(restored) != payload {
		t.Fatalf("code=%q body=%q errors=%v/%v", code, restored, err, readErr)
	}
}

func TestSensitiveTwoFactorPreservesUploadStreams(t *testing.T) {
	const secret = "JBSWY3DPEHPK3PXP"
	user := models.User{UUID: t.Name(), Username: t.Name(), Passwd: "unused-test-hash"}
	db := dbcore.GetDBInstance()
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Delete(&user) })
	code, err := totp.GenerateCode(secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, contentType     string
		twoFactor, apiKey     bool
		headerCode, queryCode string
		wantStatus            int
	}{
		{name: "disabled_raw", contentType: "application/zip", wantStatus: http.StatusNoContent},
		{name: "disabled_multipart", contentType: "multipart/form-data; boundary=test", wantStatus: http.StatusNoContent},
		{name: "disabled_json", contentType: "application/json", wantStatus: http.StatusNoContent},
		{name: "enabled_raw_missing", contentType: "application/octet-stream", twoFactor: true, wantStatus: http.StatusUnauthorized},
		{name: "enabled_multipart_missing", contentType: "multipart/form-data; boundary=test", twoFactor: true, wantStatus: http.StatusUnauthorized},
		{name: "enabled_raw_header", contentType: "application/zip", twoFactor: true, headerCode: code, wantStatus: http.StatusNoContent},
		{name: "enabled_multipart_query", contentType: "multipart/form-data; boundary=test", twoFactor: true, queryCode: code, wantStatus: http.StatusNoContent},
		{name: "api_key_exemption", contentType: "application/zip", twoFactor: true, apiKey: true, wantStatus: http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			factor := ""
			if tc.twoFactor {
				factor = secret
			}
			if err := db.Model(&user).Update("two_factor", factor).Error; err != nil {
				t.Fatal(err)
			}
			const size = 20 << 20
			body := &countingBody{remaining: size}
			r := gin.New()
			r.Use(func(c *gin.Context) {
				c.Set("uuid", user.UUID)
				if tc.apiKey {
					c.Set("api_key", "test-key")
				}
			})
			handlerCalled := false
			r.POST("/upload", RequireSensitive2FA(), func(c *gin.Context) {
				handlerCalled = true
				if body.read != 0 {
					t.Fatalf("factor check consumed %d upload bytes", body.read)
				}
				count, err := io.Copy(io.Discard, c.Request.Body)
				if err != nil || count != size {
					t.Fatalf("upload truncated: bytes=%d error=%v", count, err)
				}
				c.Status(http.StatusNoContent)
			})
			q := httptest.NewRequest(http.MethodPost, "/upload?2fa_code="+tc.queryCode, body)
			q.Header.Set("Content-Type", tc.contentType)
			q.Header.Set("X-2FA-Code", tc.headerCode)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, q)
			if w.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d", w.Code, tc.wantStatus)
			}
			if tc.wantStatus == http.StatusUnauthorized && (handlerCalled || body.read != 0) {
				t.Fatalf("denied upload consumed bytes=%d handler called=%v", body.read, handlerCalled)
			}
		})
	}
}
