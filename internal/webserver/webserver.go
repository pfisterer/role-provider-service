package webserver

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"github.com/pfisterer/role-provider-service/internal/generated_docs"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
	"github.com/pfisterer/role-provider-service/internal/helper"
	"github.com/pfisterer/role-provider-service/internal/storage"
	syncp "github.com/pfisterer/role-provider-service/internal/sync"
	"go.uber.org/zap"
)

// SetupConfig holds all dependencies needed to wire the HTTP router.
type SetupConfig struct {
	DevMode          bool
	Log              *zap.SugaredLogger
	APITokens        []string
	GroupSvc         *groupmgmt.Service
	Store            storage.Store
	SyncEngine       *syncp.Engine
	Scheduler        *syncp.Scheduler
	MaxResponseLimit int // global upper bound on list endpoints; 0 → default of 50
}

// SetupRouter configures and returns the Gin engine.
func SetupRouter(cfg SetupConfig) *gin.Engine {
	if cfg.DevMode {
		gin.SetMode(gin.TestMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()

	if cfg.DevMode {
		router.Use(disableCachingMiddleware())
	}

	// Pipe Gin logs through Zap.
	ginLogWriter := &helper.ZapWriter{SugarLogger: cfg.Log, Level: cfg.Log.Level()}
	gin.DefaultWriter = ginLogWriter
	gin.DefaultErrorWriter = ginLogWriter
	router.Use(ginzap.RecoveryWithZap(cfg.Log.Desugar(), true))

	// Documentation routes (unauthenticated).
	router.GET("/swagger.json", func(c *gin.Context) {
		c.Header("Content-Type", "application/json; charset=utf-8")
		c.String(http.StatusOK, generated_docs.SwaggerJSON)
	})
	router.GET("/", func(c *gin.Context) {
		c.Header("Content-Type", "text/html; charset=utf-8")
		c.String(http.StatusOK, swaggerUIHTML)
	})

	// CORS.
	apiGroup := router.Group("/v1")
	setupCORS(apiGroup)

	// Authentication.
	apiGroup.Use(BearerAuthMiddleware(cfg.APITokens, cfg.Log))
	apiGroup.Use(debugLoggingMiddleware(cfg.Log))

	// Route registration.
	registerGroupRoutes(apiGroup, cfg.GroupSvc, cfg.MaxResponseLimit)
	registerUserRoutes(apiGroup, cfg.GroupSvc)
	registerSyncRoutes(apiGroup, cfg.Store, cfg.SyncEngine, cfg.Scheduler)
	registerAdminRoutes(apiGroup, cfg.Store)

	return router
}

const swaggerUIHTML = `<!DOCTYPE html>
<html>
<head>
  <title>role-provider-service API</title>
  <meta charset="utf-8"/>
  <link rel="stylesheet" href="https://unpkg.com/swagger-ui-dist/swagger-ui.css">
</head>
<body>
  <div id="swagger-ui"></div>
  <script src="https://unpkg.com/swagger-ui-dist/swagger-ui-bundle.js"></script>
  <script>
    SwaggerUIBundle({ url: "/swagger.json", dom_id: '#swagger-ui', presets: [SwaggerUIBundle.presets.apis, SwaggerUIBundle.SwaggerUIStandalonePreset] })
  </script>
</body>
</html>`

func setupCORS(rg *gin.RouterGroup) {
	allowedHeaders := []string{"Origin", "Content-Type", "Authorization"}

	corsConfig := cors.Config{
		AllowOriginFunc:  func(origin string) bool { return true },
		AllowCredentials: true,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     allowedHeaders,
		MaxAge:           1 * time.Hour,
	}
	rg.Use(cors.New(corsConfig))

	rg.OPTIONS("/*path", func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", strings.Join(allowedHeaders, ", "))
		c.Header("Access-Control-Allow-Credentials", "true")
		c.Header("Access-Control-Max-Age", fmt.Sprint(int(time.Hour.Seconds())))
		c.Status(204)
	})
}

func disableCachingMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
		c.Header("Pragma", "no-cache")
		c.Header("Expires", "0")
		c.Next()
	}
}

type capturingWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *capturingWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

func debugLoggingMiddleware(log *zap.SugaredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		cw := &capturingWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = cw
		c.Next()
		path := c.Request.URL.Path
		if q := c.Request.URL.RawQuery; q != "" {
			path += "?" + q
		}
		elapsed := time.Since(start).Round(time.Millisecond)
		body := bytes.TrimSpace(cw.body.Bytes())
		if len(body) > 0 {
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, body, "  ", "  "); err == nil {
				log.Infof("%s %s (%s)\n  %s", c.Request.Method, path, elapsed, pretty.String())
			} else {
				log.Infof("%s %s (%s)", c.Request.Method, path, elapsed)
			}
		} else {
			log.Infof("%s %s (%s)", c.Request.Method, path, elapsed)
		}
	}
}
