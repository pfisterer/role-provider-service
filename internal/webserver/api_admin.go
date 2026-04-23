package webserver

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/role-provider-service/internal/storage"
)

func registerAdminRoutes(rg *gin.RouterGroup, store storage.Store) {
	a := rg.Group("/admin")
	a.GET("/health", healthCheck())
	a.GET("/stats", getStats(store))
}

// healthCheck godoc
//
//	@Summary		Health check
//	@Description	Returns 200 OK when the service is running.
//	@Tags			admin
//	@Produce		json
//	@Security		Bearer
//	@Success		200	{object}	map[string]any	"Service is healthy"
//	@Failure		401	{object}	map[string]any	"Unauthorized"
//	@ID				healthCheck
//	@Router			/v1/admin/health [get]
func healthCheck() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	}
}

type statsResponse struct {
	Groups  int64 `json:"groups"`
	Tuples  int64 `json:"tuples"`
	Sources int64 `json:"sources"`
}

// getStats godoc
//
//	@Summary		Get store statistics
//	@Description	Returns counts of groups, tuples, and sources. Only populated when using the PostgreSQL backend; returns zeros for the in-memory store.
//	@Tags			admin
//	@Produce		json
//	@Security		Bearer
//	@Success		200	{object}	statsResponse
//	@Failure		401	{object}	map[string]any	"Unauthorized"
//	@ID				getStats
//	@Router			/v1/admin/stats [get]
func getStats(store storage.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		ps, ok := store.(*storage.PostgresStore)
		if !ok {
			c.JSON(http.StatusOK, statsResponse{})
			return
		}
		resp := statsResponse{}
		ps.DB().WithContext(c.Request.Context()).Raw("SELECT COUNT(*) FROM groups").Scan(&resp.Groups)
		ps.DB().WithContext(c.Request.Context()).Raw("SELECT COUNT(*) FROM tuples").Scan(&resp.Tuples)
		ps.DB().WithContext(c.Request.Context()).Raw("SELECT COUNT(*) FROM sources").Scan(&resp.Sources)
		c.JSON(http.StatusOK, resp)
	}
}
