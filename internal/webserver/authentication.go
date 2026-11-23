package webserver

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pfisterer/cloud-self-service-golib/authn"
	"github.com/pfisterer/role-provider-service/internal/storage"
	"go.uber.org/zap"
)

// canWriteKey marks a request that authenticated with a WRITE token.
const canWriteKey = "roleProviderCanWrite"

// uploadSourceKey holds the source name of a request that authenticated with an upload token.
const uploadSourceKey = "roleProviderUploadSource"

// resolveRoute is one route a resolve token may call: Path as gin registers it, Doc as the API documents it.
type resolveRoute struct {
	Method, Path, Doc string
}

// resolveRoutes are exactly the calls the consuming services make (dynamic-zones-api, openstack-management-api, llm-management-api): a person's tokens, the group and user search for pickers, a group's resolved members for openstack-management-api's Keystone reconciler, and the configured relations. Nothing that reveals how the graph is maintained — group details with managers and viewers, sync sources and their logs, statistics — and nothing that writes.
var resolveRoutes = []resolveRoute{
	{http.MethodGet, "/v1/users/:email/tokens", "GET /v1/users/{email}/tokens"},
	{http.MethodGet, "/v1/groups", "GET /v1/groups"},
	{http.MethodGet, "/v1/groups/:token/members", "GET /v1/groups/{token}/members"},
	{http.MethodGet, "/v1/users", "GET /v1/users"},
	{http.MethodGet, "/v1/relations", "GET /v1/relations"},
}

// resolveRouteAllowed reports whether a resolve token may make this request.
func resolveRouteAllowed(c *gin.Context) bool {
	for _, r := range resolveRoutes {
		if c.Request.Method == r.Method && c.FullPath() == r.Path {
			return true
		}
	}
	return false
}

// resolveForbiddenMessage names the routes a resolve token may use, so whoever configured one sees at once why a call is refused.
func resolveForbiddenMessage() string {
	docs := make([]string, len(resolveRoutes))
	for i, r := range resolveRoutes {
		docs[i] = r.Doc
	}
	return "this API token is a resolve token and may only call " + strings.Join(docs, ", ")
}

// SourceUploadToken lets whoever holds Token replace the memberships of the one source named Source, by upload, and do nothing else.
type SourceUploadToken struct {
	Source string
	Token  string
}

// BearerAuthMiddleware validates that the request carries a known API token and
// records whether it may write.
//
// The group graph this service serves IS the authorization source for its
// consumers — whoever can write it can put themselves into group:root-admin.
// Consumers only ever read, so they get a read token; the write token stays with
// the operator who feeds the CSV. An empty write-token list therefore does not
// fall back to "everyone may write": it means nobody may.
//
// A resolve token opens only the routes the consuming services call (resolveRoutes), all of them reads; anything else is refused with 403 naming those routes. It is what a consumer should hold instead of a read token, which reads everything under /v1.
//
// An upload token (SourceUploadToken) opens exactly the upload route, and requireWriteOrOwnSource narrows that to its own source. Everything else, reads included, is refused: such a token sits on a sync server outside the cluster, and losing it must neither reveal the graph nor reach beyond the one source it feeds.
func BearerAuthMiddleware(readTokens, writeTokens, resolveTokens []string, uploadTokens []SourceUploadToken, log *zap.SugaredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if auth == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing Authorization header"})
			return
		}

		// The scheme is case-insensitive per RFC 7235.
		token := strings.TrimSpace(auth)
		if len(token) >= 7 && strings.EqualFold(token[:7], "bearer ") {
			token = strings.TrimSpace(token[7:])
		}

		canWrite := setContains(writeTokens, token)
		if !canWrite && !setContains(readTokens, token) {
			if setContains(resolveTokens, token) {
				if !resolveRouteAllowed(c) {
					log.Warnw("rejected request with a resolve token outside its routes", "remote_addr", c.ClientIP(), "method", c.Request.Method, "path", c.Request.URL.Path)
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": resolveForbiddenMessage()})
					return
				}
				c.Set(canWriteKey, false)
				c.Next()
				return
			}
			if source, ok := uploadSourceFor(uploadTokens, token); ok {
				if c.Request.Method != http.MethodPost || c.FullPath() != uploadRoute {
					log.Warnw("rejected request with an upload token outside its upload", "remote_addr", c.ClientIP(), "path", c.Request.URL.Path, "source", source)
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("this API token may only upload to source %q", source)})
					return
				}
				c.Set(canWriteKey, false)
				c.Set(uploadSourceKey, source)
				c.Next()
				return
			}
			log.Warnw("rejected request with invalid API token", "remote_addr", c.ClientIP())
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid API token"})
			return
		}
		c.Set(canWriteKey, canWrite)
		c.Next()
	}
}

// requireWriteToken guards every route that mutates the graph or its sources.
func requireWriteToken(log *zap.SugaredLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if canWrite, _ := c.Get(canWriteKey); canWrite != true {
			log.Warnw("rejected write with a read-only API token",
				"remote_addr", c.ClientIP(), "path", c.Request.URL.Path)
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "this API token may only read"})
			return
		}
		c.Next()
	}
}

// requireWriteOrOwnSource guards the upload: a write token may upload to any source, an upload token only to the source it was issued for. A source that does not exist answers the same as somebody else's, so the token cannot probe for ids.
func requireWriteOrOwnSource(store storage.Store, log *zap.SugaredLogger) gin.HandlerFunc {
	write := requireWriteToken(log)
	return func(c *gin.Context) {
		source, ok := c.Get(uploadSourceKey)
		if !ok {
			write(c)
			return
		}
		id, err := uuid.Parse(c.Param("id"))
		if err == nil {
			if src, err := store.GetSource(c.Request.Context(), id); err == nil && src != nil && src.Name == source {
				c.Next()
				return
			}
		}
		log.Warnw("rejected upload to another source", "remote_addr", c.ClientIP(), "path", c.Request.URL.Path, "source", source)
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": fmt.Sprintf("this API token may only upload to source %q", source)})
	}
}

// uploadSourceFor compares against every upload token in constant time, like setContains.
func uploadSourceFor(tokens []SourceUploadToken, token string) (string, bool) {
	source, found := "", false
	for _, t := range tokens {
		if authn.SecretEqual(token, t.Token) {
			source, found = t.Source, true
		}
	}
	return source, found
}

// setContains compares with every configured token in constant time (golib's authn.SecretEqual) and without stopping at a match, so the comparison cannot leak how much of a guessed token was correct or which entry matched. An empty configured token never matches.
func setContains(set []string, token string) bool {
	match := false
	for _, t := range set {
		if authn.SecretEqual(token, t) {
			match = true
		}
	}
	return match
}
