package webserver

import (
	"bytes"
	"encoding/json"
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
	APIWriteTokens   []string
	GroupSvc         *groupmgmt.Service
	Store            storage.Store
	SyncEngine       *syncp.Engine
	Scheduler        *syncp.Scheduler
	MaxResponseLimit int // global upper bound on list endpoints; 0 → default of 50
	// CORSAllowedOrigins are the browser origins allowed to call /v1
	// cross-origin. Empty means none — see setupCORS.
	CORSAllowedOrigins []string
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
		c.String(http.StatusOK, strings.ReplaceAll(swaggerUIHTML, "__VERSION__", generated_docs.Version))
	})

	// CORS.
	apiGroup := router.Group("/v1")
	setupCORS(apiGroup, cfg.CORSAllowedOrigins, cfg.Log)

	// Authentication.
	apiGroup.Use(BearerAuthMiddleware(cfg.APITokens, cfg.APIWriteTokens, cfg.Log))
	apiGroup.Use(debugLoggingMiddleware(cfg.Log))

	// Route registration.
	write := requireWriteToken(cfg.Log)
	registerGroupRoutes(apiGroup, cfg.GroupSvc, cfg.MaxResponseLimit, write)
	registerUserRoutes(apiGroup, cfg.GroupSvc, cfg.Store, cfg.MaxResponseLimit)
	registerSyncRoutes(apiGroup, cfg.Store, cfg.SyncEngine, cfg.Scheduler, write)
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
  <!-- __VERSION__ is replaced with the app version at serve time -->
  <footer style="position:fixed;bottom:6px;right:10px;font:12px system-ui,sans-serif;color:#888;background:rgba(255,255,255,.75);padding:2px 6px;border-radius:4px;z-index:2147483647">v__VERSION__</footer>
</body>
</html>`

// setupCORS restricts cross-origin access to allowedOrigins (exact origin
// matches). This API is consumed by other services with a static API token, not
// by a browser, so the default — an empty list, i.e. no cross-origin access at
// all — is what production runs. The previous version reflected ANY origin and
// allowed credentials, which is the combination that makes a browser hand an
// attacker page the answers to authenticated requests.
func setupCORS(rg *gin.RouterGroup, allowedOrigins []string, log *zap.SugaredLogger) {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		if trimmed := strings.TrimRight(strings.TrimSpace(origin), "/"); trimmed != "" {
			allowed[trimmed] = true
		}
	}

	if len(allowed) == 0 {
		log.Info("CORS: no allowed origins configured — cross-origin API access is disabled")
	} else {
		log.Infof("CORS: allowing cross-origin API access from %v", allowedOrigins)
	}

	rg.Use(cors.New(cors.Config{
		AllowOriginFunc:  func(origin string) bool { return allowed[strings.TrimRight(origin, "/")] },
		AllowCredentials: true,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Origin", "Content-Type", "Authorization"},
		MaxAge:           1 * time.Hour,
	}))

	// Gin runs group middleware only for requests that MATCH a route, and no
	// handler is registered for OPTIONS — without this catch-all a preflight
	// would 404 before the CORS middleware ever ran. The handler sets nothing
	// itself: an allowed origin was already answered 204 with the headers by the
	// middleware, any other origin was aborted with 403. That is the difference
	// to the previous version, whose catch-all wrote reflected headers on its own.
	rg.OPTIONS("/*path", func(c *gin.Context) { c.Status(http.StatusNoContent) })
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
