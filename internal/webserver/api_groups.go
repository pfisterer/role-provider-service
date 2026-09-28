package webserver

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
)

func registerGroupRoutes(rg *gin.RouterGroup, svc *groupmgmt.Service, maxLimit int, write gin.HandlerFunc) {
	g := rg.Group("/groups")
	g.GET("", listGroups(svc, maxLimit))
	g.POST("", write, createGroup(svc))
	g.GET("/:token", getGroup(svc))
	g.PATCH("/:token", write, updateGroup(svc))
	g.DELETE("/:token", write, deleteGroup(svc))
	g.GET("/:token/members", listMembers(svc))
	g.POST("/:token/members", write, addMember(svc))
	g.DELETE("/:token/members/*member", write, removeMember(svc))
	rg.GET("/relations", listRelations(svc))
}

// listRelations godoc
//
//	@Summary		List group relations
//	@Description	Returns the relations a group can carry, "member" first. Every relation implies membership; a user holding relation R in group G gets the token "group:G#R" in addition to "group:G".
//	@Tags			groups
//	@Produce		json
//	@Security		Bearer
//	@Success		200	{array}		string			"Relation names"
//	@Failure		401	{object}	map[string]any	"Unauthorized"
//	@ID				listRelations
//	@Router			/v1/relations [get]
func listRelations(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, svc.Relations())
	}
}

// listGroups godoc
//
//	@Summary		List groups
//	@Description	Returns groups, optionally filtered by search query and/or sync source ID.
//	@Tags			groups
//	@Produce		json
//	@Security		Bearer
//	@Param			q		query		string	false	"Search query (matches ID, display name or description)"
//	@Param			source	query		string	false	"Filter by sync source UUID"
//	@Param			limit	query		int		false	"Maximum results to return"	default(50)
//	@Success		200		{array}		common.Group
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		500		{object}	map[string]any	"Internal server error"
//	@ID				listGroups
//	@Router			/v1/groups [get]
func listGroups(svc *groupmgmt.Service, maxLimit int) gin.HandlerFunc {
	return func(c *gin.Context) {
		query := c.Query("q")
		sourceID := c.Query("source")
		limit := maxLimit
		if raw := c.Query("limit"); raw != "" {
			if n, err := strconv.Atoi(raw); err == nil && n > 0 {
				limit = n
			}
		}
		if limit > maxLimit {
			limit = maxLimit
		}
		groups, err := svc.ListGroups(c.Request.Context(), query, sourceID, limit)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, groups)
	}
}

type createGroupRequest struct {
	ID          string `json:"id" binding:"required"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// createGroup godoc
//
//	@Summary		Create group
//	@Description	Creates a new group with the given ID. The token is derived as "group:<id>".
//	@Tags			groups
//	@Accept			json
//	@Produce		json
//	@Security		Bearer
//	@Param			body	body		createGroupRequest	true	"Group to create"
//	@Success		201		{object}	common.Group
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		409		{object}	map[string]any	"Group already exists"
//	@Failure		500		{object}	map[string]any	"Internal server error"
//	@ID				createGroup
//	@Router			/v1/groups [post]
func createGroup(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createGroupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		g, err := svc.CreateGroup(c.Request.Context(), req.ID, req.DisplayName, req.Description)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusCreated, g)
	}
}

// getGroup godoc
//
//	@Summary		Get group
//	@Description	Returns a single group by its token (e.g. "group:dept_cs").
//	@Tags			groups
//	@Produce		json
//	@Security		Bearer
//	@Param			token	path		string	true	"Group token (e.g. group:dept_cs)"
//	@Success		200		{object}	common.Group
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		404		{object}	map[string]any	"Not found"
//	@ID				getGroup
//	@Router			/v1/groups/{token} [get]
func getGroup(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		g, err := svc.GetGroup(c.Request.Context(), c.Param("token"))
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, g)
	}
}

type updateGroupRequest struct {
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// updateGroup godoc
//
//	@Summary		Update group
//	@Description	Updates the display name and/or description of a group.
//	@Tags			groups
//	@Accept			json
//	@Produce		json
//	@Security		Bearer
//	@Param			token	path		string				true	"Group token"
//	@Param			body	body		updateGroupRequest	true	"Fields to update"
//	@Success		200		{object}	common.Group
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		404		{object}	map[string]any	"Not found"
//	@Failure		500		{object}	map[string]any	"Internal server error"
//	@ID				updateGroup
//	@Router			/v1/groups/{token} [patch]
func updateGroup(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req updateGroupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		g, err := svc.UpdateGroup(c.Request.Context(), c.Param("token"), req.DisplayName, req.Description)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, g)
	}
}

// deleteGroup godoc
//
//	@Summary		Delete group
//	@Description	Permanently deletes a group and all its member tuples.
//	@Tags			groups
//	@Security		Bearer
//	@Param			token	path	string	true	"Group token"
//	@Success		204		"No content"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		404		{object}	map[string]any	"Not found"
//	@Failure		500		{object}	map[string]any	"Internal server error"
//	@ID				deleteGroup
//	@Router			/v1/groups/{token} [delete]
func deleteGroup(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.DeleteGroup(c.Request.Context(), c.Param("token")); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// listMembers godoc
//
//	@Summary		List group members
//	@Description	Returns the members of a group holding a relation (default "member", i.e. everyone in the group — every relation implies membership). By default resolves members transitively (recursive=true); a sub-group contributes all of its members. Set recursive=false for direct members only.
//	@Tags			groups
//	@Produce		json
//	@Security		Bearer
//	@Param			token		path		string	true	"Group token"
//	@Param			recursive	query		bool	false	"Expand sub-groups recursively"	default(true)
//	@Param			relation	query		string	false	"Relation to list (see /v1/relations)"	default(member)
//	@Success		200			{array}		string	"List of member tokens"
//	@Failure		400			{object}	map[string]any	"Unknown relation"
//	@Failure		401			{object}	map[string]any	"Unauthorized"
//	@Failure		404			{object}	map[string]any	"Not found"
//	@Failure		500			{object}	map[string]any	"Internal server error"
//	@ID				listGroupMembers
//	@Router			/v1/groups/{token}/members [get]
func listMembers(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		recursive := c.Query("recursive") != "false"
		members, err := svc.GetAllMembers(c.Request.Context(), c.Param("token"), c.Query("relation"), recursive)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, members)
	}
}

type addMemberRequest struct {
	Member string `json:"member" binding:"required"`
	// Relation the member gets in the group; empty means "member".
	Relation string `json:"relation,omitempty"`
}

// addMember godoc
//
//	@Summary		Add member to group
//	@Description	Adds a user or group token to the group with a relation (default "member"). A group token as member means all of that group's members hold the relation.
//	@Tags			groups
//	@Accept			json
//	@Security		Bearer
//	@Param			token	path	string			true	"Group token"
//	@Param			body	body	addMemberRequest	true	"Member token to add (e.g. user:email or group:id)"
//	@Success		204		"No content"
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		404		{object}	map[string]any	"Group not found"
//	@Failure		500		{object}	map[string]any	"Internal server error"
//	@ID				addGroupMember
//	@Router			/v1/groups/{token}/members [post]
func addMember(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req addMemberRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := svc.AddMember(c.Request.Context(), c.Param("token"), req.Relation, req.Member); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// removeMember godoc
//
//	@Summary		Remove member from group
//	@Description	Removes one relation (default "member") of a user or group token from the group. Other relations the member holds stay.
//	@Tags			groups
//	@Security		Bearer
//	@Param			token	path	string	true	"Group token"
//	@Param			member	path	string	true	"Member token to remove (e.g. user:email or group:id)"
//	@Param			relation	query	string	false	"Relation to remove"	default(member)
//	@Success		204		"No content"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		404		{object}	map[string]any	"Not found"
//	@Failure		500		{object}	map[string]any	"Internal server error"
//	@ID				removeGroupMember
//	@Router			/v1/groups/{token}/members/{member} [delete]
func removeMember(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		member := c.Param("member")
		// Strip leading slash that Gin adds for wildcard params.
		if len(member) > 0 && member[0] == '/' {
			member = member[1:]
		}
		if err := svc.RemoveMember(c.Request.Context(), c.Param("token"), c.Query("relation"), member); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}
