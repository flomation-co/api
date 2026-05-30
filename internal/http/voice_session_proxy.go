package http

// Voice session WebSocket proxy — bridges executor ↔ Launch for
// Twilio voice call audio streaming.
//
// Route: GET /api/v1/internal/voice-session/:session_id
//
// The executor connects here via mTLS. The API proxies the WebSocket
// to Launch's internal voice-session endpoint. This keeps executors
// isolated in the private network — they never contact Launch directly.

import (
	"fmt"
	gohttp "net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	log "github.com/sirupsen/logrus"
)

var wsUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *gohttp.Request) bool { return true },
}

// handleVoiceSessionProxy handles GET /api/v1/internal/voice-session/:session_id
// Accepts a WebSocket from the executor and connects it to Launch's
// internal voice-session WebSocket, creating a bidirectional bridge.
func (s *Service) handleVoiceSessionProxy(c *gin.Context) {
	sessionID := c.Param("session_id")
	if sessionID == "" {
		c.AbortWithStatus(gohttp.StatusBadRequest)
		return
	}

	// Upgrade the executor connection to WebSocket
	executorConn, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		log.WithError(err).Error("voice proxy: failed to upgrade executor WebSocket")
		return
	}
	defer func() { _ = executorConn.Close() }()

	// Connect to Launch's internal voice-session WebSocket
	launchURL := s.buildLaunchVoiceWSURL(sessionID)
	if launchURL == "" {
		log.Error("voice proxy: Launch URL not configured")
		return
	}

	dialer := websocket.Dialer{}
	// Use the Launch connector's mTLS transport
	launchClient := s.launch.Client()
	if transport, ok := launchClient.Transport.(*gohttp.Transport); ok && transport.TLSClientConfig != nil {
		dialer.TLSClientConfig = transport.TLSClientConfig
	}

	launchConn, _, err := dialer.Dial(launchURL, nil)
	if err != nil {
		log.WithFields(log.Fields{
			"session_id": sessionID,
			"launch_url": launchURL,
			"error":      err,
		}).Error("voice proxy: failed to connect to Launch voice session")
		return
	}
	defer func() { _ = launchConn.Close() }()

	log.WithField("session_id", sessionID).Info("voice session proxy established")

	// Bidirectional bridge
	done := make(chan struct{}, 2)

	// Executor → Launch
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			msgType, data, err := executorConn.ReadMessage()
			if err != nil {
				return
			}
			if err := launchConn.WriteMessage(msgType, data); err != nil {
				return
			}
		}
	}()

	// Launch → Executor
	go func() {
		defer func() { done <- struct{}{} }()
		for {
			msgType, data, err := launchConn.ReadMessage()
			if err != nil {
				return
			}
			if err := executorConn.WriteMessage(msgType, data); err != nil {
				return
			}
		}
	}()

	// Wait for either direction to finish
	<-done

	log.WithField("session_id", sessionID).Info("voice session proxy closed")
}

// buildLaunchVoiceWSURL constructs the WebSocket URL for Launch's internal
// voice-session endpoint.
func (s *Service) buildLaunchVoiceWSURL(sessionID string) string {
	launchURL := s.config.InternalLaunchURL()
	if launchURL == "" {
		return ""
	}

	wsURL := strings.Replace(launchURL, "https://", "wss://", 1)
	wsURL = strings.Replace(wsURL, "http://", "ws://", 1)
	return fmt.Sprintf("%s/internal/voice-session/%s", wsURL, sessionID)
}