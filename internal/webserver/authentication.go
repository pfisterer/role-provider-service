package webserver

import (
	"crypto/subtle"
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/storage"
	"go.uber.org/zap"
)

// canWriteKey marks a request that authenticated with a WRITE token.
const canWriteKey = "roleProviderCanWrite"

// uploadSourceKey holds the source name of a request that authenticated with an upload token.
const uploadSourceKey = "roleProviderUploadSource"

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
// An upload token (SourceUploadToken) opens exactly the upload route, and requireWriteOrOwnSource narrows that to its own source. Everything else, reads included, is refused: such a token sits on a sync server outside the cluster, and losing it must neither reveal the graph nor reach beyond the one source it feeds.
func BearerAuthMiddleware(readTokens, writeTokens []string, uploadTokens []SourceUploadToken, log *zap.SugaredLogger) gin.HandlerFunc {
	readSet := newTokenSet(readTokens)
	writeSet := newTokenSet(writeTokens)

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

		canWrite := setContains(writeSet, token)
		if !canWrite && !setContains(readSet, token) {
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
	candidate := []byte(token)
	source, found := "", false
	for _, t := range tokens {
		if t.Token != "" && subtle.ConstantTimeCompare([]byte(t.Token), candidate) == 1 {
			source, found = t.Source, true
		}
	}
	return source, found
}

func newTokenSet(tokens []string) [][]byte {
	set := make([][]byte, 0, len(tokens))
	for _, t := range tokens {
		if t != "" {
			set = append(set, []byte(t))
		}
	}
	return set
}

// setContains compares in constant time per candidate, so the comparison cannot
// leak how much of a guessed token was correct.
func setContains(set [][]byte, token string) bool {
	candidate := []byte(token)
	match := false
	for _, t := range set {
		if subtle.ConstantTimeCompare(t, candidate) == 1 {
			match = true
		}
	}
	return match
}
