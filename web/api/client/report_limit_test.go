package client

import (
	"bytes"
	"compress/gzip"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAgentBodyLimitAlsoBoundsGzipExpansion(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		for _, size := range []int{256, maxAgentRequestBytes, maxAgentRequestBytes + 1} {
			plain := strings.Repeat("x", size)
			body := []byte(plain)
			if compressed {
				var buf bytes.Buffer
				zw := gzip.NewWriter(&buf)
				_, _ = zw.Write(body)
				_ = zw.Close()
				body = buf.Bytes()
			}
			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body))
			if compressed {
				req.Header.Set("Content-Encoding", "gzip")
			}
			got, err := readMaybeCompressedBody(req)
			if size > maxAgentRequestBytes {
				if !errors.Is(err, errAgentRequestTooLarge) || got != nil {
					t.Fatalf("oversized body accepted (gzip=%v)", compressed)
				}
			} else if err != nil || len(got) != size {
				t.Fatalf("valid body: gzip=%v size=%d err=%v", compressed, size, err)
			}
		}
	}
}
