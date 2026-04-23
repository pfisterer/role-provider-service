package webserver

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
)

func registerUserRoutes(rg *gin.RouterGroup, svc *groupmgmt.Service) {
	rg.GET("/users/:email/tokens", getUserTokens(svc))
}

// getUserTokens godoc
//
//	@Summary		Get user tokens
//	@Description	Returns all tokens for the given email: the user's own token ("user:<email>") plus all group tokens resolved transitively.
//	@Tags			users
//	@Produce		json
//	@Security		Bearer
//	@Param			email	path		string	true	"User email address"
//	@Success		200		{array}		string	"List of tokens (user: and group: prefixed)"
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		500		{object}	map[string]any	"Internal server error"
//	@ID				getUserTokens
//	@Router			/v1/users/{email}/tokens [get]
func getUserTokens(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		email := strings.TrimSpace(c.Param("email"))
		if email == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "email must not be empty"})
			return
		}
		tokens, err := svc.GetUserTokens(c.Request.Context(), email)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, tokens)
	}
}
