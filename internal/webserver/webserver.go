package webserver

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	ginzap "github.com/gin-contrib/zap"
	"github.com/gin-gonic/gin"
	"github.com/pfisterer/cloud-self-service-golib/ginweb"
	"github.com/pfisterer/cloud-self-service-golib/logging"
	"github.com/pfisterer/role-provider-service/internal/generated_docs"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
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
	// cross-origin. Empty means none — see ginweb.EnableCORS.
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
		router.Use(ginweb.DisableCaching())
	}

	// Pipe Gin logs through Zap.
	ginLogWriter := &logging.Writer{Logger: cfg.Log, Level: cfg.Log.Level()}
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
		c.String(http.StatusOK, strings.ReplaceAll(indexHTML, "__VERSION__", generated_docs.Version))
	})

	// CORS.
	apiGroup := router.Group("/v1")
	ginweb.EnableCORS(apiGroup, ginweb.CORSOptions{
		AllowedOrigins: cfg.CORSAllowedOrigins,
		DevMode:        cfg.DevMode,
		AllowMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		Log:            cfg.Log,
	})

	// Authentication.
	apiGroup.Use(BearerAuthMiddleware(cfg.APITokens, cfg.APIWriteTokens, cfg.Log))
	// Response bodies here are group memberships: names and mail addresses of
	// real people. That belongs in a local debug session, never in the cluster's
	// central log storage — so this middleware exists only in dev mode.
	if cfg.DevMode {
		apiGroup.Use(debugLoggingMiddleware(cfg.Log))
	} else {
		apiGroup.Use(accessLoggingMiddleware(cfg.Log))
	}

	// Route registration.
	write := requireWriteToken(cfg.Log)
	registerGroupRoutes(apiGroup, cfg.GroupSvc, cfg.MaxResponseLimit, write)
	registerUserRoutes(apiGroup, cfg.GroupSvc, cfg.Store, cfg.MaxResponseLimit)
	registerSyncRoutes(apiGroup, cfg.Store, cfg.SyncEngine, cfg.Scheduler, write)
	registerAdminRoutes(apiGroup, cfg.Store)

	return router
}

// indexHTML is a plain landing page. It deliberately embeds NO third-party
// script: the previous version pulled swagger-ui from unpkg.com on every load —
// an unpinned, unverified script from a foreign host running on one of our own
// origins. The spec is served next door as swagger.json; point any local
// swagger-ui / editor at it, or read it with curl.
const indexHTML = `<!DOCTYPE html>
<html>
<head>
  <title>role-provider-service API</title>
  <meta charset="utf-8"/>
  <meta name="viewport" content="width=device-width, initial-scale=1"/>
  <style>
    body { font: 15px/1.5 system-ui, sans-serif; margin: 3rem auto; max-width: 42rem; padding: 0 1rem; color: #222; }
    code { background: #f2f2f2; padding: .1rem .3rem; border-radius: 3px; }
    footer { margin-top: 3rem; color: #888; font-size: 12px; }
    @media (prefers-color-scheme: dark) {
      body { background: #111; color: #eee; }
      code { background: #222; }
      a { color: #7ab7ff; }
    }
  </style>
</head>
<body>
  <h1>role-provider-service</h1>
  <p>Group and role directory. Every <code>/v1</code> endpoint requires an API token:
     <code>Authorization: Bearer &lt;token&gt;</code>. Read tokens may query the graph;
     changing groups, memberships or sync sources needs a write token.</p>
  <p>API specification: <a href="swagger.json">swagger.json</a> (OpenAPI 2.0).</p>
  <footer>v__VERSION__</footer>
</body>
</html>`

type capturingWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (w *capturingWriter) Write(b []byte) (int, error) {
	w.body.Write(b)
	return w.ResponseWriter.Write(b)
}

// accessLoggingMiddleware is the production counterpart of
// debugLoggingMiddleware: same one line per request, but without the response
// body, which on this service is personal data.
func accessLoggingMiddleware(log *zap.SugaredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Infof("%s %s → %d (%s)", c.Request.Method, requestPath(c),
			c.Writer.Status(), time.Since(start).Round(time.Millisecond))
	}
}

// requestPath is the request path with its query string, if any.
func requestPath(c *gin.Context) string {
	path := c.Request.URL.Path
	if q := c.Request.URL.RawQuery; q != "" {
		path += "?" + q
	}
	return path
}

// debugLoggingMiddleware additionally logs the response body. Dev mode only —
// see the wiring in SetupRouter.
func debugLoggingMiddleware(log *zap.SugaredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		cw := &capturingWriter{ResponseWriter: c.Writer, body: &bytes.Buffer{}}
		c.Writer = cw
		c.Next()
		path := requestPath(c)
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
