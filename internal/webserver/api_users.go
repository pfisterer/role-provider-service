package webserver

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
	"github.com/pfisterer/role-provider-service/internal/storage"
)

func registerUserRoutes(rg *gin.RouterGroup, svc *groupmgmt.Service, store storage.Store, maxLimit int) {
	rg.GET("/users", searchUsers(store, maxLimit))
	rg.GET("/users/:email/tokens", getUserTokens(svc))
}

// searchUsers godoc
//
//	@Summary		Search users
//	@Description	Returns email addresses matching the query, case-insensitively. Matching is on the address only — the store holds no names — so this cannot be used to look people up by name. Members that exist only through a pattern rule have no row and are not returned.
//	@Tags			users
//	@Produce		json
//	@Security		Bearer
//	@Param			q		query		string	false	"Case-insensitive substring of the email address"
//	@Param			limit	query		int		false	"Maximum results to return"	default(50)
//	@Success		200		{array}		string	"Matching email addresses"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		500		{object}	map[string]any	"Internal server error"
//	@ID				searchUsers
//	@Router			/v1/users [get]
func searchUsers(store storage.Store, maxLimit int) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit, err := strconv.Atoi(c.DefaultQuery("limit", strconv.Itoa(maxLimit)))
		if err != nil || limit < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid limit"})
			return
		}
		if limit == 0 || limit > maxLimit {
			limit = maxLimit
		}
		emails, err := store.SearchUsers(c.Request.Context(), c.Query("q"), limit)
		if err != nil {
			respondError(c, err)
			return
		}
		if emails == nil {
			emails = []string{}
		}
		c.JSON(http.StatusOK, emails)
	}
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
