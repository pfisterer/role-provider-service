package webserver

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// BearerAuthMiddleware validates that the request carries a known API token.
func BearerAuthMiddleware(validTokens []string, log *zap.SugaredLogger) gin.HandlerFunc {
	tokenSet := make(map[string]struct{}, len(validTokens))
	for _, t := range validTokens {
		if t != "" {
			tokenSet[t] = struct{}{}
		}
	}

	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		if auth == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "missing Authorization header"})
			return
		}

		token := strings.TrimPrefix(auth, "Bearer ")
		token = strings.TrimSpace(token)

		if _, ok := tokenSet[token]; !ok {
			log.Warnw("rejected request with invalid API token", "remote_addr", c.ClientIP())
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "invalid API token"})
			return
		}
		c.Next()
	}
}
