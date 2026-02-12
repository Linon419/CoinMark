package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"coinmark/api-go/internal/hub"
)

func registerHubRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/hub/market", handleHubWS(d))
}

func handleHubWS(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		if d.Hub == nil || !d.Cfg.HubEnabled {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "hub disabled"})
			return
		}

		// origin check
		origin := c.Request.Header.Get("Origin")
		if !checkOrigin(origin, d.Cfg.HubAllowedOrigins) {
			c.JSON(http.StatusForbidden, gin.H{"error": "origin not allowed"})
			return
		}

		ws, err := wsUpgrader.Upgrade(c.Writer, c.Request, nil)
		if err != nil {
			return
		}

		connID := uuid.New().String()
		if !d.Hub.Manager.Connect(connID, ws) {
			ws.WriteJSON(hub.ErrorMsg{Kind: "error", Message: "max connections reached"})
			ws.Close()
			return
		}
		defer d.Hub.Manager.Disconnect(connID)

		// send connected
		ws.WriteJSON(hub.ConnectedMsg{Kind: "connected", ConnectionID: connID, Ts: time.Now().UnixMilli()})

		for {
			_, msg, err := ws.ReadMessage()
			if err != nil {
				break
			}
			d.Hub.Manager.Touch(connID)

			op, err := hub.ParseClientOp(msg)
			if err != nil {
				ws.WriteJSON(hub.ErrorMsg{Kind: "error", Message: "invalid json"})
				continue
			}

			switch op.Op {
			case "ping":
				ws.WriteJSON(hub.PingMsg{Kind: "pong", Ts: time.Now().UnixMilli()})

			case "subscribe":
				d.Hub.Manager.UpdateSubscription(connID, op.Markets, op.Symbols, op.Types)
				markets, symbols, types := d.Hub.Manager.GetSubscription(connID)
				ws.WriteJSON(hub.SubscribedMsg{
					Kind: "subscribed", Markets: markets, Symbols: symbols, Types: types,
					Ts: time.Now().UnixMilli(),
				})

			default:
				ws.WriteJSON(hub.ErrorMsg{Kind: "error", Message: "unknown op: " + op.Op})
			}
		}
	}
}

func checkOrigin(origin, allowed string) bool {
	if allowed == "" || allowed == "*" {
		return true
	}
	if origin == "" {
		return true
	}
	for _, a := range strings.Split(allowed, ",") {
		a = strings.TrimSpace(a)
		if a == "*" || a == origin {
			return true
		}
	}
	return false
}

// unused but kept for potential debug
func _hubDebugLog(msg []byte) {
	var m map[string]json.RawMessage
	json.Unmarshal(msg, &m)
	log.Printf("hub ws: %s", string(msg))
}
