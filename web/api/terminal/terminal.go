package terminal

import (
	"sync"
	"time"

	"github.com/komari-monitor/komari/web/connection"
)

type TerminalSession struct {
	UUID                    string
	UserUUID                string
	Browser                 *connection.SafeConn
	Agent                   *connection.SafeConn
	RequesterIp             string
	Forwarding              bool
	CleanupTimer            *time.Timer
	BrowserCredentialsValid func() bool
	AgentCredentialsValid   func() bool
}

var TerminalSessionsMutex = &sync.Mutex{}
var TerminalSessions = make(map[string]*TerminalSession)

// 与 v2 事件队列 TTL 对齐，被控端短暂离线后会话仍可恢复。
const terminalSessionRetention = 5 * time.Minute

func scheduleCleanup(id string, session *TerminalSession) {
	if session.CleanupTimer != nil {
		session.CleanupTimer.Stop()
	}
	session.CleanupTimer = time.AfterFunc(terminalSessionRetention, func() {
		var browser, agent *connection.SafeConn

		TerminalSessionsMutex.Lock()
		if current, ok := TerminalSessions[id]; !ok || current != session {
			TerminalSessionsMutex.Unlock()
			return
		}
		browser, agent = session.Browser, session.Agent
		delete(TerminalSessions, id)
		TerminalSessionsMutex.Unlock()

		if browser != nil {
			_ = browser.Close()
		}
		if agent != nil {
			_ = agent.Close()
		}
	})
}

func stopCleanup(session *TerminalSession) {
	if session.CleanupTimer != nil {
		session.CleanupTimer.Stop()
		session.CleanupTimer = nil
	}
}

func suspendSession(id string, browser, agent *connection.SafeConn) {
	var otherBrowser, otherAgent *connection.SafeConn

	TerminalSessionsMutex.Lock()
	session, ok := TerminalSessions[id]
	if !ok || session == nil ||
		(browser != nil && session.Browser != browser) ||
		(agent != nil && session.Agent != agent) {
		TerminalSessionsMutex.Unlock()
		return
	}
	otherBrowser, otherAgent = session.Browser, session.Agent
	session.Browser = nil
	session.Agent = nil
	session.Forwarding = false
	scheduleCleanup(id, session)
	TerminalSessionsMutex.Unlock()

	if otherBrowser != nil {
		_ = otherBrowser.Close()
	}
	if otherAgent != nil {
		_ = otherAgent.Close()
	}
}

func closeSession(id string) {
	closeSessionIfCurrent(id, nil, nil)
}

// A forwarding goroutine may finish after a browser has reattached. Never let
// a stale connection or its credential watcher close that replacement.
func closeSessionIfCurrent(id string, expectedBrowser, expectedAgent *connection.SafeConn) {
	var browser, agent *connection.SafeConn

	TerminalSessionsMutex.Lock()
	if session, ok := TerminalSessions[id]; ok && session != nil {
		if (expectedBrowser != nil && session.Browser != expectedBrowser) ||
			(expectedAgent != nil && session.Agent != expectedAgent) {
			TerminalSessionsMutex.Unlock()
			return
		}
		stopCleanup(session)
		browser, agent = session.Browser, session.Agent
		delete(TerminalSessions, id)
	}
	TerminalSessionsMutex.Unlock()

	if browser != nil {
		_ = browser.Close()
	}
	if agent != nil {
		_ = agent.Close()
	}
}

func attachBrowser(id, userUUID string, apiKey bool, conn *connection.SafeConn, credentialsValid func() bool) (*TerminalSession, bool) {
	TerminalSessionsMutex.Lock()
	session, ok := TerminalSessions[id]
	if !ok || session == nil {
		TerminalSessionsMutex.Unlock()
		return nil, false
	}
	if !apiKey && session.UserUUID != userUUID {
		TerminalSessionsMutex.Unlock()
		return nil, false
	}
	oldBrowser := session.Browser
	session.Browser = conn
	session.BrowserCredentialsValid = credentialsValid
	session.Forwarding = false
	if session.Agent != nil {
		stopCleanup(session)
	}
	TerminalSessionsMutex.Unlock()
	if oldBrowser != nil && oldBrowser != conn {
		_ = oldBrowser.Close()
	}
	return session, true
}

func attachAgent(id string, conn *connection.SafeConn, credentialsValid func() bool) (*TerminalSession, bool) {
	TerminalSessionsMutex.Lock()
	session, ok := TerminalSessions[id]
	if !ok || session == nil {
		TerminalSessionsMutex.Unlock()
		return nil, false
	}
	oldAgent := session.Agent
	session.Agent = conn
	session.AgentCredentialsValid = credentialsValid
	session.Forwarding = false
	if session.Browser != nil {
		stopCleanup(session)
	}
	TerminalSessionsMutex.Unlock()
	if oldAgent != nil && oldAgent != conn {
		_ = oldAgent.Close()
	}
	return session, true
}

func maybeStartForwarding(id string) {
	TerminalSessionsMutex.Lock()
	session, ok := TerminalSessions[id]
	if !ok || session == nil || session.Browser == nil || session.Agent == nil || session.Forwarding {
		TerminalSessionsMutex.Unlock()
		return
	}
	session.Forwarding = true
	browser, agent := session.Browser, session.Agent
	TerminalSessionsMutex.Unlock()
	go ForwardTerminal(id, browser, agent)
}
