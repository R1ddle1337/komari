package terminal

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/komari-monitor/komari/database/auditlog"
	"github.com/komari-monitor/komari/web/connection"
)

func ForwardTerminal(id string, browser, agent *connection.SafeConn) {
	if browser == nil || agent == nil {
		return
	}
	TerminalSessionsMutex.Lock()
	session := TerminalSessions[id]
	requesterIp, userUUID := "", ""
	var browserCredentialsValid, agentCredentialsValid func() bool
	if session != nil && session.Browser == browser && session.Agent == agent {
		requesterIp, userUUID = session.RequesterIp, session.UserUUID
		browserCredentialsValid, agentCredentialsValid = session.BrowserCredentialsValid, session.AgentCredentialsValid
	}
	TerminalSessionsMutex.Unlock()
	if requesterIp == "" {
		return
	}
	credentialsValid := func() bool {
		return browserCredentialsValid != nil && agentCredentialsValid != nil &&
			browserCredentialsValid() && agentCredentialsValid()
	}
	closeCurrentSession := func() { closeSessionIfCurrent(id, browser, agent) }
	if !credentialsValid() {
		closeCurrentSession()
		return
	}

	auditlog.Log(requesterIp, userUUID, "established, terminal id:"+id, "terminal")
	established_time := time.Now()
	errChan := make(chan error, 2)
	done := make(chan struct{})
	defer close(done)
	reportError := func(err error) {
		select {
		case errChan <- err:
		case <-done:
		}
	}
	// Also close idle/output-only terminals promptly when their login session,
	// API key, or agent token is revoked. Input is revalidated before forwarding.
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				if !credentialsValid() {
					closeCurrentSession()
					reportError(errors.New("terminal credentials expired or revoked"))
					return
				}
			}
		}
	}()

	go func() {
		for {
			messageType, data, err := browser.ReadMessage()
			if err != nil {
				reportError(err)
				return
			}
			if !credentialsValid() {
				closeCurrentSession()
				reportError(errors.New("terminal credentials expired or revoked"))
				return
			}

			if messageType == websocket.TextMessage {
				var control struct {
					Type string `json:"type"`
				}
				if json.Unmarshal(data, &control) == nil {
					if control.Type == "heartbeat" {
						continue
					}
					if control.Type == "close" {
						_ = agent.WriteJSON(gin.H{"type": "close"})
						closeCurrentSession()
						reportError(nil)
						return
					}
				}
				if len(data) > 0 && data[0] == '{' {
					err = agent.WriteMessage(websocket.TextMessage, data)
				} else {
					err = agent.WriteMessage(websocket.BinaryMessage, data)
				}
			} else {
				err = agent.WriteMessage(websocket.BinaryMessage, data)
			}

			if err != nil {
				reportError(err)
				return
			}
		}
	}()

	go func() {
		for {
			_, data, err := agent.ReadMessage()
			if err != nil {
				reportError(err)
				return
			}
			err = browser.WriteMessage(websocket.BinaryMessage, data)
			if err != nil {
				reportError(err)
				return
			}
		}
	}()

	// 等待错误或主动关闭
	<-errChan
	suspendSession(id, browser, agent)
	disconnect_time := time.Now()
	auditlog.Log(requesterIp, userUUID, "disconnected, terminal id:"+id+", duration:"+disconnect_time.Sub(established_time).String(), "terminal")
}
