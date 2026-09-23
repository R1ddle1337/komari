package admin

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"runtime"
	runtimepprof "runtime/pprof"
	"runtime/trace"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/komari-monitor/komari/utils"
	"github.com/komari-monitor/komari/web/api"
)

const (
	defaultPprofDurationSeconds = 10
	minPprofDurationSeconds     = 1
	maxPprofDurationSeconds     = 30
	maxPprofPreviewBytes        = 1 << 20
)

var (
	profileProcessStarted = time.Now()
	cpuProfileMu          sync.Mutex
	traceProfileMu        sync.Mutex

	errPprofBusy            = errors.New("pprof collection is already in progress")
	errPprofUnavailable     = errors.New("pprof profile is not available")
	errPprofPreviewTooLarge = errors.New("pprof text preview exceeds the size limit")
)

type pprofTarget struct {
	Name     string
	Duration time.Duration
}

// pprofPreviewBuffer keeps automatic browser previews bounded. Binary profile
// downloads remain available for profiles that exceed this compact view.
type pprofPreviewBuffer struct {
	data      bytes.Buffer
	limit     int
	truncated bool
}

func (buffer *pprofPreviewBuffer) Write(data []byte) (int, error) {
	remaining := buffer.limit - buffer.data.Len()
	if remaining <= 0 {
		if len(data) == 0 {
			return 0, nil
		}
		buffer.truncated = true
		return 0, errPprofPreviewTooLarge
	}
	if len(data) > remaining {
		buffer.truncated = true
		_, _ = buffer.data.Write(data[:remaining])
		return remaining, errPprofPreviewTooLarge
	}
	return buffer.data.Write(data)
}

type pprofProfileInfo struct {
	Name     string `json:"name"`
	Endpoint string `json:"endpoint"`
	Samples  int    `json:"samples,omitempty"`
	Timed    bool   `json:"timed,omitempty"`
	Preview  string `json:"preview,omitempty"`
}

type pprofMemorySummary struct {
	HeapAlloc   uint64 `json:"heap_alloc"`
	HeapInuse   uint64 `json:"heap_inuse"`
	HeapObjects uint64 `json:"heap_objects"`
	Sys         uint64 `json:"sys"`
}

type pprofSummary struct {
	Build struct {
		Version       string `json:"version"`
		Revision      string `json:"revision"`
		GoVersion     string `json:"go_version"`
		UptimeSeconds int64  `json:"uptime_seconds"`
	} `json:"build"`
	Profiles []pprofProfileInfo `json:"profiles"`
	Runtime  struct {
		Goroutines int                `json:"goroutines"`
		Memory     pprofMemorySummary `json:"memory"`
	} `json:"runtime"`
	Duration struct {
		DefaultSeconds int `json:"default_seconds"`
		MinSeconds     int `json:"min_seconds"`
		MaxSeconds     int `json:"max_seconds"`
	} `json:"duration"`
}

var runtimeProfileNames = []string{
	"allocs",
	"block",
	"goroutine",
	"heap",
	"mutex",
	"threadcreate",
}

// RegisterPprofRoutes adds diagnostic endpoints to an already administrator-
// protected route group. It deliberately uses runtime/pprof directly instead
// of net/http/pprof, which would register unauthenticated /debug/pprof routes
// on http.DefaultServeMux as a package side effect.
func RegisterPprofRoutes(group *gin.RouterGroup) {
	pprofGroup := group.Group("/pprof")
	pprofGroup.GET("/summary", pprofSummaryHandler)
	pprofGroup.GET("/profile", downloadCPUProfile)
	pprofGroup.GET("/trace", downloadTrace)
	for _, name := range runtimeProfileNames {
		pprofGroup.GET("/"+name, downloadRuntimeProfile(name))
	}
}

// pprofSummaryHandler returns the current profile inventory and inexpensive
// runtime counters for the administrator diagnostic page.
func pprofSummaryHandler(c *gin.Context) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	response := pprofSummary{
		Profiles: make([]pprofProfileInfo, 0, len(runtimeProfileNames)+2),
	}
	response.Profiles = append(response.Profiles,
		pprofProfileInfo{
			Name:     "cpu",
			Endpoint: "/api/admin/pprof/profile",
			Timed:    true,
		},
		pprofProfileInfo{
			Name:     "trace",
			Endpoint: "/api/admin/pprof/trace",
			Timed:    true,
		},
	)
	for _, name := range runtimeProfileNames {
		profile := runtimepprof.Lookup(name)
		if profile == nil {
			continue
		}
		response.Profiles = append(response.Profiles, pprofProfileInfo{
			Name:     name,
			Endpoint: "/api/admin/pprof/" + name,
			Samples:  profile.Count(),
			Preview:  "/api/admin/pprof/" + name + "?format=text",
		})
	}

	response.Runtime.Goroutines = runtime.NumGoroutine()
	response.Build.Version = utils.CurrentVersion
	response.Build.Revision = utils.VersionHash
	response.Build.GoVersion = runtime.Version()
	response.Build.UptimeSeconds = int64(time.Since(profileProcessStarted).Seconds())
	response.Runtime.Memory = pprofMemorySummary{
		HeapAlloc:   mem.HeapAlloc,
		HeapInuse:   mem.HeapInuse,
		HeapObjects: mem.HeapObjects,
		Sys:         mem.Sys,
	}
	response.Duration.DefaultSeconds = defaultPprofDurationSeconds
	response.Duration.MinSeconds = minPprofDurationSeconds
	response.Duration.MaxSeconds = maxPprofDurationSeconds

	api.RespondSuccess(c, response)
}

// downloadCPUProfile records a bounded CPU profile. CPU profiling is process-
// global, so a concurrent collection is rejected instead of queued.
func downloadCPUProfile(c *gin.Context) {
	if !requireBinaryPprof(c) {
		return
	}
	duration, ok := pprofDuration(c)
	if !ok {
		return
	}
	downloadPprofTarget(c, pprofTarget{Name: "cpu", Duration: duration})
}

// downloadTrace records a bounded execution trace. Runtime tracing is also a
// process-global facility and therefore cannot run concurrently.
func downloadTrace(c *gin.Context) {
	if !requireBinaryPprof(c) {
		return
	}
	duration, ok := pprofDuration(c)
	if !ok {
		return
	}
	downloadPprofTarget(c, pprofTarget{Name: "trace", Duration: duration})
}

// downloadRuntimeProfile returns a runtime profile as a binary download by
// default, or as the safe debug=1 textual summary requested by the admin UI.
func downloadRuntimeProfile(name string) gin.HandlerFunc {
	return func(c *gin.Context) {
		debug, ok := pprofRuntimeDebug(c)
		if !ok {
			return
		}
		target := pprofTarget{Name: name}
		if debug > 0 {
			previewPprofTarget(c, target)
			return
		}
		downloadPprofTarget(c, target)
	}
}

func downloadPprofTarget(c *gin.Context, target pprofTarget) {
	preparePprofResponse(c, pprofFilename(target), 0)
	if err := collectPprof(c.Request.Context(), target, c.Writer, 0); err != nil && !c.Writer.Written() {
		respondPprofCollectionError(c, err)
	}
}

func previewPprofTarget(c *gin.Context, target pprofTarget) {
	writePprofPreview(c, target, collectPprof)
}

func writePprofPreview(c *gin.Context, target pprofTarget, collect func(context.Context, pprofTarget, io.Writer, int) error) {
	buffer := &pprofPreviewBuffer{limit: maxPprofPreviewBytes}
	if err := collect(c.Request.Context(), target, buffer, 1); err != nil && !errors.Is(err, errPprofPreviewTooLarge) {
		respondPprofCollectionError(c, err)
		return
	}
	preparePprofResponse(c, pprofFilename(target), 1)
	preview := buffer.data.Bytes()
	// Some runtime profiles ignore errors from their tabwriter. Track overflow
	// in the writer itself so these previews are also explicitly marked.
	c.Header("X-Pprof-Preview-Truncated", strconv.FormatBool(buffer.truncated))
	if buffer.truncated {
		marker := []byte("\n[Preview truncated; download the pprof file for the complete profile.]\n")
		preview = preview[:buffer.limit-len(marker)]
		if end := bytes.LastIndexByte(preview, '\n'); end >= 0 {
			preview = preview[:end]
		}
		preview = append(preview, marker...)
	}
	c.Data(http.StatusOK, "text/plain; charset=utf-8", preview)
}

func collectPprof(ctx context.Context, target pprofTarget, writer io.Writer, debug int) error {
	switch target.Name {
	case "cpu":
		if !cpuProfileMu.TryLock() {
			return errPprofBusy
		}
		defer cpuProfileMu.Unlock()
		if err := runtimepprof.StartCPUProfile(writer); err != nil {
			return errPprofBusy
		}
		defer runtimepprof.StopCPUProfile()
		waitForPprofDuration(ctx, target.Duration)
		return nil
	case "trace":
		if !traceProfileMu.TryLock() {
			return errPprofBusy
		}
		defer traceProfileMu.Unlock()
		if err := trace.Start(writer); err != nil {
			return errPprofBusy
		}
		defer trace.Stop()
		waitForPprofDuration(ctx, target.Duration)
		return nil
	default:
		if !isRuntimeProfile(target.Name) {
			return errPprofUnavailable
		}
		profile := runtimepprof.Lookup(target.Name)
		if profile == nil {
			return errPprofUnavailable
		}
		return profile.WriteTo(writer, debug)
	}
}

func pprofRuntimeDebug(c *gin.Context) (int, bool) {
	format, hasFormat, valid := singlePprofQuery(c, "format")
	if !valid || (hasFormat && format != "text") {
		api.RespondError(c, http.StatusBadRequest, "format must be text when provided")
		return 0, false
	}
	debug, hasDebug, valid := singlePprofQuery(c, "debug")
	if !valid || (hasDebug && debug != "1") {
		api.RespondError(c, http.StatusBadRequest, "debug must be 1 when provided")
		return 0, false
	}
	if hasFormat || hasDebug {
		return 1, true
	}
	return 0, true
}

func requireBinaryPprof(c *gin.Context) bool {
	for _, key := range []string{"format", "debug"} {
		_, present, valid := singlePprofQuery(c, key)
		if !valid || present {
			api.RespondError(c, http.StatusBadRequest, "CPU and trace profiles only support binary downloads")
			return false
		}
	}
	return true
}

func singlePprofQuery(c *gin.Context, key string) (string, bool, bool) {
	values, present := c.GetQueryArray(key)
	if !present {
		return "", false, true
	}
	if len(values) != 1 {
		return "", true, false
	}
	return values[0], true, true
}

func isRuntimeProfile(name string) bool {
	for _, profile := range runtimeProfileNames {
		if name == profile {
			return true
		}
	}
	return false
}

func pprofFilename(target pprofTarget) string {
	if target.Name == "trace" {
		return "trace.out"
	}
	return target.Name + ".pprof"
}

func respondPprofCollectionError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, errPprofBusy):
		api.RespondError(c, http.StatusConflict, "profile collection is already in progress")
	case errors.Is(err, errPprofUnavailable):
		api.RespondError(c, http.StatusNotFound, "profile is not available")
	case errors.Is(err, errPprofPreviewTooLarge):
		api.RespondError(c, http.StatusRequestEntityTooLarge, "profile text preview is too large; download the pprof file instead")
	default:
		api.RespondError(c, http.StatusInternalServerError, "failed to collect profile")
	}
}

func pprofDuration(c *gin.Context) (time.Duration, bool) {
	values, present := c.GetQueryArray("seconds")
	if !present {
		return time.Duration(defaultPprofDurationSeconds) * time.Second, true
	}
	if len(values) != 1 {
		api.RespondError(c, http.StatusBadRequest, "seconds must be provided once")
		return 0, false
	}

	seconds, err := strconv.Atoi(values[0])
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, fmt.Sprintf(
			"seconds must be an integer between %d and %d",
			minPprofDurationSeconds,
			maxPprofDurationSeconds,
		))
		return 0, false
	}
	duration, err := pprofDurationFromSeconds(seconds)
	if err != nil {
		api.RespondError(c, http.StatusBadRequest, err.Error())
		return 0, false
	}
	return duration, true
}

func pprofDurationFromSeconds(seconds int) (time.Duration, error) {
	if seconds < minPprofDurationSeconds || seconds > maxPprofDurationSeconds {
		return 0, fmt.Errorf(
			"seconds must be an integer between %d and %d",
			minPprofDurationSeconds,
			maxPprofDurationSeconds,
		)
	}
	return time.Duration(seconds) * time.Second, nil
}

func preparePprofResponse(c *gin.Context, filename string, debug int) {
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if debug > 0 {
		c.Header("Content-Type", "text/plain; charset=utf-8")
		return
	}
	c.Header("Content-Type", "application/octet-stream")
	c.Header("Content-Disposition", `attachment; filename="`+filename+`"`)
}

func waitForPprofDuration(ctx context.Context, duration time.Duration) {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}
