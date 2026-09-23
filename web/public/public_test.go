package public

import (
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/internal/config"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestNormalizeHTMLLanguage(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		"hyphen language": {
			input: "zh-CN",
			want:  "zh-CN",
		},
		"underscore language": {
			input: "zh_CN",
			want:  "zh-CN",
		},
		"reject script injection": {
			input: `zh-CN" autofocus`,
		},
		"reject too short": {
			input: "z",
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := normalizeHTMLLanguage(tt.input); got != tt.want {
				t.Fatalf("normalizeHTMLLanguage(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestReplaceHTMLLanguage(t *testing.T) {
	tests := map[string]struct {
		html     string
		language string
		want     string
	}{
		"replace existing lang": {
			html:     `<html lang="en"><head></head></html>`,
			language: "zh-CN",
			want:     `<html lang="zh-CN"><head></head></html>`,
		},
		"insert missing lang": {
			html:     `<html><head></head></html>`,
			language: "ja_JP",
			want:     `<html lang="ja-JP"><head></head></html>`,
		},
		"ignore invalid lang": {
			html:     `<html lang="en"><head></head></html>`,
			language: `zh-CN" autofocus`,
			want:     `<html lang="en"><head></head></html>`,
		},
	}

	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if got := replaceHTMLLanguage(tt.html, tt.language); got != tt.want {
				t.Fatalf("replaceHTMLLanguage() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestEmbeddedDistDoesNotEmbedRawFiles(t *testing.T) {
	if _, err := PublicFS.ReadFile("defaultTheme/dist/index.html"); err == nil {
		t.Fatal("PublicFS still embeds the raw frontend files")
	}
	targetDir := filepath.Join(t.TempDir(), "dist")
	if err := extractDistArchive(embeddedDistArchive, targetDir); err != nil {
		t.Fatalf("extract embedded dist: %v", err)
	}
	content, err := os.ReadFile(filepath.Join(targetDir, IndexFile))
	if err != nil || len(content) == 0 {
		t.Fatalf("embedded dist does not contain a non-empty %q", IndexFile)
	}
}

func TestStaticRestrictedDoesNotServeCustomAssetOverride(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldDefaultDistCacheDir := defaultDistCacheDir
	defaultDistCacheDir = filepath.Join(t.TempDir(), "dist")
	if err := extractDistArchive(embeddedDistArchive, defaultDistCacheDir); err != nil {
		t.Fatalf("extract embedded dist: %v", err)
	}
	t.Cleanup(func() {
		defaultDistCacheDir = oldDefaultDistCacheDir
	})
	t.Chdir(t.TempDir())
	assetPath := filepath.Join("data", "theme", "custom", "dist", "assets")
	if err := os.MkdirAll(assetPath, 0o755); err != nil {
		t.Fatalf("create custom theme asset directory: %v", err)
	}
	const assetName = "about-D4JKo971.css"
	if err := os.MkdirAll(filepath.Join(defaultDistCacheDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(defaultDistCacheDir, "assets", assetName), []byte("default asset"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(assetPath, assetName), []byte("custom override"), 0o644); err != nil {
		t.Fatalf("write custom theme asset: %v", err)
	}

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open config db: %v", err)
	}
	config.SetDb(db)
	if err := config.Set(config.ThemeKey, "custom"); err != nil {
		t.Fatalf("set custom theme: %v", err)
	}

	router := gin.New()
	StaticRestricted(router.Group("/"), func(handlers ...gin.HandlerFunc) {
		router.NoRoute(handlers...)
	})
	for _, requestPath := range []string{"/assets/" + assetName} {
		request := httptest.NewRequest("GET", requestPath, nil)
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		if recorder.Code != 200 {
			t.Fatalf("restricted asset %s status = %d, want 200", requestPath, recorder.Code)
		}
		body, err := io.ReadAll(recorder.Result().Body)
		if err != nil {
			t.Fatalf("read restricted asset %s: %v", requestPath, err)
		}
		if string(body) != "default asset" {
			t.Fatalf("restricted listener did not serve the default asset for %s: %s", requestPath, body)
		}
	}

	indexRequest := httptest.NewRequest("GET", "/database-recovery", nil)
	indexRecorder := httptest.NewRecorder()
	router.ServeHTTP(indexRecorder, indexRequest)
	indexBody, err := io.ReadAll(indexRecorder.Result().Body)
	if err != nil {
		t.Fatalf("read restricted index: %v", err)
	}
	if strings.Contains(string(indexBody), `vite-plugin-pwa:register-sw`) {
		t.Fatal("restricted index still registers a service worker")
	}
}

func TestStaticUpgradeCachePolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldDefaultDistCacheDir := defaultDistCacheDir
	defaultDistCacheDir = filepath.Join(t.TempDir(), "dist")
	t.Cleanup(func() { defaultDistCacheDir = oldDefaultDistCacheDir })
	t.Chdir(t.TempDir())
	for name, body := range map[string]string{
		filepath.Join(defaultDistCacheDir, "index.html"):             `<html><head><script type="module" src="/assets/app-AbCd1234.js"></script><script id="vite-plugin-pwa:register-sw" src="/registerSW.js"></script></head><body>admin</body></html>`,
		filepath.Join(defaultDistCacheDir, "assets/app-AbCd1234.js"): `console.log("default")`,
		"data/theme/custom/dist/index.html":                          `<html><head></head><body>custom</body></html>`,
		"data/theme/custom/dist/assets/app-AbCd1234.js":              `console.log("custom")`,
		"data/theme/custom/dist/manifest.webmanifest":                `{}`,
	} {
		if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(name, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	config.SetDb(db)
	if err := config.Set(config.ThemeKey, "custom"); err != nil {
		t.Fatal(err)
	}
	router := gin.New()
	Static(router.Group("/"), func(handlers ...gin.HandlerFunc) { router.NoRoute(handlers...) })
	for _, tt := range []struct {
		path                         string
		status                       int
		cache, contentType, contains string
	}{
		{"/api", 404, "no-store", "application/json", "API endpoint not found"},
		{"/api/clients", 404, "no-store", "application/json", "API endpoint not found"},
		{"/api/records/load", 404, "no-store", "application/json", "API endpoint not found"},
		{"/api/clients/report", 404, "no-store", "application/json", "API endpoint not found"},
		{"/instance/node-a", 200, "no-store", "text/html", "custom"},
		{"/admin/dashboard", 200, "no-store", "text/html", `src="/themes/default/dist/assets/app-AbCd1234.js"`},
		{"/", 200, "no-store", "text/html", "custom"},
		{"/assets/old-AbCd1234.js", 404, "no-store", "text/plain", "Asset not found"},
		{"/assets/old.css", 404, "no-store", "text/plain", "Asset not found"},
		{"/themes/default/dist/assets/old.js", 404, "no-store", "", ""},
		{"/themes/default/dist/assets/app-AbCd1234.js", 200, "public, max-age=31536000, immutable", "text/javascript", "default"},
		{"/assets/app-AbCd1234.js", 200, "public, max-age=31536000, immutable", "text/javascript", "custom"},
		{"/manifest.webmanifest", 200, "no-cache", "application/manifest+json", "{}"},
		{"/sw.js", 200, "no-store", "text/javascript", "skipWaiting"},
		{"/komari-sw.js?v=1", 200, "no-store", "text/javascript", "workbox-precache-"},
	} {
		t.Run(tt.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", tt.path, nil))
			if w.Code != tt.status || w.Header().Get("Cache-Control") != tt.cache || !strings.HasPrefix(w.Header().Get("Content-Type"), tt.contentType) || !strings.Contains(w.Body.String(), tt.contains) {
				t.Fatalf("unexpected response: status=%d headers=%v body=%s", w.Code, w.Header(), w.Body.String())
			}
			if tt.cache == "no-store" && w.Header().Get("CDN-Cache-Control") != "no-store" {
				t.Fatal("CDN can cache a dynamic/error response")
			}
			if strings.HasPrefix(tt.contentType, "text/html") && strings.Contains(w.Body.String(), `vite-plugin-pwa:register-sw`) {
				t.Fatal("legacy worker registration survives migration")
			}
		})
	}
}
