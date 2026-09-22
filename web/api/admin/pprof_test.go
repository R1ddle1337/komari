package admin

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestPprofPreviewBounds(t *testing.T) {
	for _, tc := range []struct {
		name        string
		size        int
		ignoreError bool
	}{
		{"small", 128, false},
		{"exact limit", maxPprofPreviewBytes, false},
		{"overflow", maxPprofPreviewBytes + 1, false},
		{"runtime ignores write error", maxPprofPreviewBytes * 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/heap?format=text", nil)
			writePprofPreview(c, pprofTarget{Name: "heap"}, func(_ context.Context, _ pprofTarget, out io.Writer, debug int) error {
				if debug != 1 {
					t.Fatalf("unsafe debug mode: %d", debug)
				}
				_, err := out.Write([]byte(strings.Repeat("x", tc.size)))
				if tc.ignoreError {
					return nil
				}
				return err
			})
			if w.Code != http.StatusOK {
				t.Fatalf("status: %d", w.Code)
			}
			if w.Body.Len() > maxPprofPreviewBytes {
				t.Fatalf("unbounded preview: %d", w.Body.Len())
			}
			truncated := tc.size > maxPprofPreviewBytes
			if (w.Header().Get("X-Pprof-Preview-Truncated") == "true") != truncated {
				t.Fatal("incorrect truncation header")
			}
			if strings.Contains(w.Body.String(), "Preview truncated") != truncated {
				t.Fatal("incorrect truncation notice")
			}
			if !truncated && w.Body.Len() != tc.size {
				t.Fatal("complete preview changed")
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("profile must not be cached")
			}
		})
	}
}

func TestPprofPreviewDoesNotHideCollectionErrors(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/heap?format=text", nil)
	writePprofPreview(c, pprofTarget{Name: "heap"}, func(_ context.Context, _ pprofTarget, out io.Writer, _ int) error {
		_, _ = out.Write([]byte("partial data"))
		return errors.New("collection failed")
	})
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: %d", w.Code)
	}
}

func TestPprofBufferRepeatedWrites(t *testing.T) {
	b := &pprofPreviewBuffer{limit: 4}
	_, _ = b.Write([]byte("1234"))
	if _, err := b.Write(nil); err != nil || b.truncated {
		t.Fatal("empty write marked truncated")
	}
	if n, err := b.Write([]byte("5")); n != 0 || !errors.Is(err, errPprofPreviewTooLarge) {
		t.Fatal("overflow accepted")
	}
	if b.data.String() != "1234" || !b.truncated {
		t.Fatal("invalid bounded result")
	}
}
