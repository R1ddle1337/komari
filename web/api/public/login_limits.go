package public

import (
	"net/netip"
	"sync"
	"time"

	"github.com/komari-monitor/komari/web/security"
)

const (
	maxLoginBytes   = security.MaxLoginBodyBytes
	loginWindow     = time.Minute
	loginPerIP      = 10
	loginGlobal     = 120
	maxLoginSources = 4096
)

type loginWindowState struct {
	start    time.Time
	attempts int
}
type loginLimiter struct {
	mu      sync.Mutex
	sources map[string]loginWindowState
	global  loginWindowState
}

var passwordLoginLimiter = &loginLimiter{sources: make(map[string]loginWindowState)}
var passwordLoginSlots = make(chan struct{}, 2)

func (limiter *loginLimiter) allow(source string, now time.Time) bool {
	// Group IPv6 privacy addresses within one network to prevent a trivial
	// bypass without retaining usernames, passwords or other credentials.
	if address, err := netip.ParseAddr(source); err == nil {
		address = address.Unmap()
		if address.Is6() {
			source = netip.PrefixFrom(address, 64).Masked().String()
		} else {
			source = address.String()
		}
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if now.Sub(limiter.global.start) >= loginWindow {
		limiter.global = loginWindowState{start: now}
	}
	state, exists := limiter.sources[source]
	if !exists || now.Sub(state.start) >= loginWindow {
		state = loginWindowState{start: now}
	}
	if state.attempts >= loginPerIP || limiter.global.attempts >= loginGlobal {
		return false
	}
	if !exists && len(limiter.sources) >= maxLoginSources {
		for key, state := range limiter.sources {
			if now.Sub(state.start) >= loginWindow {
				delete(limiter.sources, key)
			}
		}
		if len(limiter.sources) >= maxLoginSources {
			return false
		}
	}
	state.attempts++
	limiter.sources[source] = state
	limiter.global.attempts++
	return true
}
