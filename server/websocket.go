// //////////////////////////////////////////////////////////////////////
// David Gurley
// dmgurley@gmail.com
// June 2018
//
// //////////////////////////////////////////////////////////////////////
package server

import (
	"net/http"

	"github.com/gorilla/websocket"

	"github.com/bboxsys/platform/logger"
)

// allowedOrigins is the WebSocket origin allowlist. Configure it for the
// deployment with SetAllowedOrigins.
var allowedOrigins = map[string]bool{
	"https://localhost": true,
	"https://127.0.0.1": true,
}

// allowAllWebsocketOrigins disables origin checking entirely. It is intended
// for local development only and must never be enabled in production.
var allowAllWebsocketOrigins = false

// SetAllowedOrigins replaces the WebSocket origin allowlist.
func SetAllowedOrigins(origins []string) {
	m := make(map[string]bool, len(origins))
	for _, o := range origins {
		m[o] = true
	}
	allowedOrigins = m
}

// SetAllowAllWebsocketOrigins disables WebSocket origin validation. Development
// use only; leaving this on in production is a cross-origin (CSRF) hole.
func SetAllowAllWebsocketOrigins(allow bool) {
	allowAllWebsocketOrigins = allow
}

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	// Validate origin to prevent cross-origin WebSocket hijacking (CSRF).
	CheckOrigin: func(r *http.Request) bool {
		if allowAllWebsocketOrigins {
			return true
		}
		origin := r.Header.Get("Origin")
		if origin == "" {
			// No Origin header: a non-browser or same-origin client. Browsers
			// always send Origin for cross-origin requests, so this cannot be
			// used to bypass the allowlist from a malicious page.
			return true
		}
		return allowedOrigins[origin]
	},
}

// implementation of http.Handler
type Websocket struct {
	Options *WebsocketOptions
	Logger  *logger.Logger
}

func NewWebsocket(options *WebsocketOptions) *Websocket {
	l := logger.NewLogger(options.Application, "websocket")
	return &Websocket{
		Options: options,
		Logger:  l,
	}
}

func (websocket *Websocket) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		websocket.Logger.ErrorE().Err(err).Msg("error with connection")
		return
	}
	websocket.Logger.InfoE().Str("remoteAddr", conn.RemoteAddr().String()).Msg("websocket client connected")
	defer conn.Close()
	for {
		mt, message, err := conn.ReadMessage()
		if err != nil {
			websocket.Logger.ErrorE().Err(err).Msg("websocket read error")
			break
		}
		websocket.Logger.InfoE().Str("message", string(message)).Msg("websocket recv")
		err = conn.WriteMessage(mt, message)
		if err != nil {
			websocket.Logger.ErrorE().Err(err).Msg("websocket write error")
			break
		}
	}
}
