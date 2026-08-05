package webserver

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// canWriteKey marks a request that authenticated with a WRITE token.
const canWriteKey = "roleProviderCanWrite"

// BearerAuthMiddleware validates that the request carries a known API token and
// records whether it may write.
//
// The group graph this service serves IS the authorization source for its
// consumers — whoever can write it can put themselves into group:root-admin.
// Consumers only ever read, so they get a read token; the write token stays with
// the operator who feeds the CSV. An empty write-token list therefore does not
// fall back to "everyone may write": it means nobody may.
func BearerAuthMiddleware(readTokens, writeTokens []string, log *zap.SugaredLogger) gin.HandlerFunc {
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
