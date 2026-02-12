package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"
)

func registerUserRoutes(g *gin.RouterGroup, d *Deps) {
	g.GET("/user/favorites", handleGetFavorites(d))
	g.POST("/user/favorites", handleUpsertFavorites(d))
	g.DELETE("/user/favorites/:symbol", handleDeleteFavorite(d))
}

func handleGetFavorites(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		clientID := c.Query("clientId")
		if len(clientID) < 8 || len(clientID) > 64 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "clientId must be 8-64 chars"})
			return
		}
		market := c.Query("market")
		limit := queryInt(c, "limit", 200, 1, 2000)

		args := []interface{}{clientID}
		where := "client_id = ?"
		if market == "spot" || market == "swap" {
			where += " AND market = ?"
			args = append(args, market)
		}
		args = append(args, limit)
		q := `SELECT market, symbol, created_at FROM favorites WHERE ` + where + ` ORDER BY created_at DESC LIMIT ?`

		type row struct {
			Market    string `db:"market" json:"market"`
			Symbol    string `db:"symbol" json:"symbol"`
			CreatedAt string `db:"created_at" json:"createdAt"`
		}
		var rows []row
		if err := d.Store.SelectContext(c.Request.Context(), &rows, q, args...); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if rows == nil {
			rows = []row{}
		}
		c.JSON(http.StatusOK, gin.H{"clientId": clientID, "items": rows})
	}
}

func handleUpsertFavorites(d *Deps) gin.HandlerFunc {
	type body struct {
		Market  string   `json:"market"`
		Symbols []string `json:"symbols"`
	}
	return func(c *gin.Context) {
		clientID := c.Query("clientId")
		if len(clientID) < 8 || len(clientID) > 64 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "clientId must be 8-64 chars"})
			return
		}
		var b body
		if err := c.ShouldBindJSON(&b); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		m := strings.ToLower(b.Market)
		if m != "spot" && m != "swap" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "market must be spot or swap"})
			return
		}
		if len(b.Symbols) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "symbols required"})
			return
		}

		sql := `INSERT INTO favorites (client_id, market, symbol) VALUES (?, ?, ?) ON CONFLICT(client_id, market, symbol) DO NOTHING`
		var added []string
		err := d.Store.Write(c.Request.Context(), func(_ context.Context, tx *sqlx.Tx) error {
			for _, sym := range b.Symbols {
				sym = strings.ToUpper(strings.TrimSpace(sym))
				if sym == "" {
					continue
				}
				res, err := tx.Exec(sql, clientID, m, sym)
				if err != nil {
					return err
				}
				n, _ := res.RowsAffected()
				if n > 0 {
					added = append(added, sym)
				}
			}
			return nil
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "clientId": clientID, "market": m, "added": added})
	}
}

func handleDeleteFavorite(d *Deps) gin.HandlerFunc {
	return func(c *gin.Context) {
		symbol := strings.ToUpper(c.Param("symbol"))
		clientID := c.Query("clientId")
		market := strings.ToLower(c.Query("market"))
		if len(clientID) < 8 || len(clientID) > 64 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "clientId must be 8-64 chars"})
			return
		}
		if market != "spot" && market != "swap" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "market must be spot or swap"})
			return
		}
		var deleted int
		err := d.Store.Write(c.Request.Context(), func(_ context.Context, tx *sqlx.Tx) error {
			res, err := tx.Exec("DELETE FROM favorites WHERE client_id = ? AND market = ? AND symbol = ?", clientID, market, symbol)
			if err != nil {
				return err
			}
			n, _ := res.RowsAffected()
			deleted = int(n)
			return nil
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"ok": true, "clientId": clientID, "market": market, "symbol": symbol, "deleted": deleted})
	}
}
