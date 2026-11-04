package webserver

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
)

// registerManageRoutes serves the people who maintain groups of the local source, through the self-service UI. Every route runs behind personAuthMiddleware and judges the request by callerOf.
func registerManageRoutes(rg *gin.RouterGroup, svc *groupmgmt.Service, sourceName string, maxLimit int) {
	rg.GET("/me", manageMe(svc, sourceName))
	rg.GET("/principals", manageSearchPrincipals(svc, maxLimit))
	rg.GET("/sources", manageListSources(svc))
	g := rg.Group("/groups")
	g.GET("", manageListGroups(svc))
	g.POST("", manageCreateGroup(svc))
	g.GET("/:group", manageGetGroup(svc))
	g.PATCH("/:group", manageUpdateGroup(svc))
	g.DELETE("/:group", manageDeleteGroup(svc))
	g.POST("/:group/members", manageAddMember(svc))
	g.DELETE("/:group/members", manageRemoveMember(svc))
	g.PATCH("/:group/members", manageChangeRelation(svc))
	g.GET("/:group/effective", manageEffectiveMembers(svc))
	g.POST("/:group/managers", manageAddManager(svc))
	g.DELETE("/:group/managers", manageRemoveManager(svc))
	g.GET("/:group/history", manageHistory(svc))
}

// manageMeResponse is who the caller is and what the group pages may offer them.
type manageMeResponse struct {
	// Actor is the person signed in.
	Actor string `json:"actor"`
	// ActingAs is the person acted for (X-Act-As); empty otherwise.
	ActingAs string `json:"acting_as,omitempty"`
	// MayActAs: the signed-in person may act on behalf of another person.
	MayActAs bool `json:"may_act_as"`
	// Admin may maintain every group of the local source, create and delete groups.
	Admin bool `json:"admin"`
	// ManagesAny: the caller maintains at least one group, or is an admin — whether the UI offers the section at all.
	ManagesAny bool `json:"manages_any"`
	// Relations a member can hold, "member" first.
	Relations []string `json:"relations"`
	// SourceName is the name of the local source, shown next to its groups.
	SourceName string `json:"source_name"`
}

// manageMe godoc
//
//	@Summary		Who the caller is
//	@Description	The signed-in person, whom the request acts for, and whether they maintain groups here at all.
//	@Tags			manage
//	@Produce		json
//	@Security		Bearer
//	@Success		200	{object}	manageMeResponse
//	@Failure		401	{object}	map[string]any	"Not signed in"
//	@ID				manageGetMe
//	@Router			/v1/manage/me [get]
func manageMe(svc *groupmgmt.Service, sourceName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		caller := callerOf(c)
		managesAny := caller.Admin
		if !managesAny {
			groups, err := svc.ManagedGroups(c.Request.Context(), caller, "")
			if err != nil {
				respondError(c, err)
				return
			}
			managesAny = len(groups) > 0
		}
		c.JSON(http.StatusOK, manageMeResponse{
			Actor:      caller.Actor,
			ActingAs:   caller.OnBehalfOf,
			MayActAs:   c.GetBool(mayActAsKey),
			Admin:      caller.Admin,
			ManagesAny: managesAny,
			Relations:  svc.Relations(),
			SourceName: sourceName,
		})
	}
}

// manageSearchPrincipals godoc
//
//	@Summary		Search groups and people
//	@Description	What a member or manager field may be filled with: groups of every source, each relation as an entry of its own, and e-mail addresses — those only for a non-empty query, the directory is not browsable. For people who maintain a group.
//	@Tags			manage
//	@Produce		json
//	@Security		Bearer
//	@Param			q		query		string	false	"Search query"
//	@Param			limit	query		int		false	"Maximum entries per kind"	default(50)
//	@Success		200		{object}	groupmgmt.Principals
//	@Failure		401		{object}	map[string]any	"Not signed in"
//	@Failure		403		{object}	map[string]any	"Maintains no group"
//	@ID				manageSearchPrincipals
//	@Router			/v1/manage/principals [get]
func manageSearchPrincipals(svc *groupmgmt.Service, maxLimit int) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := maxLimit
		if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 && n < maxLimit {
			limit = n
		}
		p, err := svc.SearchPrincipals(c.Request.Context(), callerOf(c), c.Query("q"), limit)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, p)
	}
}

// manageListGroups godoc
//
//	@Summary		Groups to browse
//	@Description	For an admin, every source: with path_to, the way from the top of that group's source tree down to it (top first, the group last, through groups of its source); the groups entered in parent; else those matching q, optionally of one source; else the top of a source's tree (its groups no other group of that source contains); with none of these the local groups. Everybody else gets the local groups they maintain, narrowed by q. Each group says whether the caller may change it (editable: local groups only) and how many groups it holds.
//	@Tags			manage
//	@Produce		json
//	@Security		Bearer
//	@Param			q		query		string	false	"Search query"
//	@Param			source	query		string	false	"Source name (admins)"
//	@Param			parent	query		string	false	"Group whose groups to list (admins)"
//	@Param			path_to	query		string	false	"Group to find in the tree: the groups from the top of its source down to it (admins)"
//	@Param			limit	query		int		false	"Page size"	default(200)
//	@Param			offset	query		int		false	"Entries to skip"
//	@Success		200		{object}	groupmgmt.GroupPage
//	@Failure		401		{object}	map[string]any	"Not signed in"
//	@Failure		404		{object}	map[string]any	"No such parent"
//	@ID				manageListGroups
//	@Router			/v1/manage/groups [get]
func manageListGroups(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		q := groupmgmt.BrowseQuery{Query: c.Query("q"), Source: c.Query("source"), Parent: c.Query("parent"), PathTo: c.Query("path_to"), Limit: 200}
		if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 && n <= 1000 {
			q.Limit = n
		}
		if n, err := strconv.Atoi(c.Query("offset")); err == nil && n > 0 {
			q.Offset = n
		}
		page, err := svc.Browse(c.Request.Context(), callerOf(c), q)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, page)
	}
}

// manageListSources godoc
//
//	@Summary		Every source
//	@Description	The sources with their number of groups and last sync, the local one first: its groups are maintained here, the others are read-only copies of their sync. Admins only.
//	@Tags			manage
//	@Produce		json
//	@Security		Bearer
//	@Success		200	{array}		groupmgmt.SourceSummary
//	@Failure		401	{object}	map[string]any	"Not signed in"
//	@Failure		403	{object}	map[string]any	"Not an admin"
//	@ID				manageListSources
//	@Router			/v1/manage/sources [get]
func manageListSources(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		sources, err := svc.Sources(c.Request.Context(), callerOf(c))
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, sources)
	}
}

// manageCreateGroup godoc
//
//	@Summary		Create a group
//	@Description	Creates a group of the local source. Admins only. An id another source reserves is refused (409).
//	@Tags			manage
//	@Accept			json
//	@Produce		json
//	@Security		Bearer
//	@Param			body	body		createGroupRequest	true	"The group"
//	@Success		201		{object}	common.Group
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		403		{object}	map[string]any	"Not an admin"
//	@Failure		409		{object}	map[string]any	"Exists or reserved"
//	@ID				manageCreateGroup
//	@Router			/v1/manage/groups [post]
func manageCreateGroup(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createGroupRequest
		if !bindJSON(c, &req) {
			return
		}
		g, err := svc.CreateGroup(c.Request.Context(), callerOf(c), req.ID, req.DisplayName, req.Description, req.Managers)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusCreated, g)
	}
}

// manageGetGroup godoc
//
//	@Summary		A group with its entries and managers
//	@Description	A group the caller maintains, or for an admin any group (a sync source's read-only, editable false): its direct entries with relation and expiry (expired ones included and marked), and who maintains it.
//	@Tags			manage
//	@Produce		json
//	@Security		Bearer
//	@Param			group	path		string	true	"Group id"
//	@Success		200		{object}	groupmgmt.GroupDetail
//	@Failure		403		{object}	map[string]any	"Not maintained by the caller"
//	@Failure		404		{object}	map[string]any	"Not found"
//	@Failure		409		{object}	map[string]any	"Maintained by a sync source"
//	@ID				manageGetGroup
//	@Router			/v1/manage/groups/{group} [get]
func manageGetGroup(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		d, err := svc.Detail(c.Request.Context(), callerOf(c), c.Param("group"))
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, d)
	}
}

// manageUpdateGroup godoc
//
//	@Summary		Rename or describe a group
//	@Description	Sets the display name and description of a group the caller maintains.
//	@Tags			manage
//	@Accept			json
//	@Produce		json
//	@Security		Bearer
//	@Param			group	path		string				true	"Group id"
//	@Param			body	body		updateGroupRequest	true	"Display name and description"
//	@Success		200		{object}	common.Group
//	@Failure		403		{object}	map[string]any	"Not maintained by the caller"
//	@Failure		404		{object}	map[string]any	"Not found"
//	@ID				manageUpdateGroup
//	@Router			/v1/manage/groups/{group} [patch]
func manageUpdateGroup(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req updateGroupRequest
		if !bindJSON(c, &req) {
			return
		}
		g, err := svc.UpdateGroup(c.Request.Context(), callerOf(c), c.Param("group"), req.DisplayName, req.Description)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, g)
	}
}

// manageDeleteGroup godoc
//
//	@Summary		Delete a group
//	@Description	Deletes a group of the local source with its entries, its memberships in other groups and its managers. Admins only.
//	@Tags			manage
//	@Security		Bearer
//	@Param			group	path	string	true	"Group id"
//	@Success		204		"Deleted"
//	@Failure		403		{object}	map[string]any	"Not an admin"
//	@Failure		404		{object}	map[string]any	"Not found"
//	@ID				manageDeleteGroup
//	@Router			/v1/manage/groups/{group} [delete]
func manageDeleteGroup(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.DeleteGroup(c.Request.Context(), callerOf(c), c.Param("group")); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// manageAddMember godoc
//
//	@Summary		Add an entry
//	@Description	Adds a person (user:<email>), a group (group:<id>) or a pattern (pattern:*@<domain>) to a group the caller maintains, in a relation (default "member"), optionally until expires_at. Adding an entry that exists sets its expiry. A group that would end up containing itself is refused (409).
//	@Tags			manage
//	@Accept			json
//	@Security		Bearer
//	@Param			group	path	string				true	"Group id"
//	@Param			body	body	addMemberRequest	true	"The entry"
//	@Success		204		"Added"
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		403		{object}	map[string]any	"Not maintained by the caller"
//	@Failure		409		{object}	map[string]any	"Cycle, or maintained by a sync source"
//	@ID				manageAddMember
//	@Router			/v1/manage/groups/{group}/members [post]
func manageAddMember(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req addMemberRequest
		if !bindJSON(c, &req) {
			return
		}
		if err := svc.AddMember(c.Request.Context(), callerOf(c), c.Param("group"), req.Relation, req.Member, req.ExpiresAt); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// manageRemoveMember godoc
//
//	@Summary		Remove an entry
//	@Description	Removes one relation (default "member") of an entry from a group the caller maintains. Other relations the entry holds stay.
//	@Tags			manage
//	@Security		Bearer
//	@Param			group		path	string	true	"Group id"
//	@Param			member		query	string	true	"Entry token: user:<email>, group:<id> or pattern:<glob>"
//	@Param			relation	query	string	false	"Relation"	default(member)
//	@Success		204			"Removed"
//	@Failure		403			{object}	map[string]any	"Not maintained by the caller"
//	@ID				manageRemoveMember
//	@Router			/v1/manage/groups/{group}/members [delete]
func manageRemoveMember(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.RemoveMember(c.Request.Context(), callerOf(c), c.Param("group"), c.Query("relation"), c.Query("member")); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

type changeRelationRequest struct {
	// Member is the entry's token: user:<email>, group:<id> or pattern:<glob>.
	Member string `json:"member" binding:"required"`
	// Relation is the entry's relation now; empty means "member".
	Relation string `json:"relation"`
	// NewRelation is the relation it gets; empty means "member".
	NewRelation string `json:"new_relation"`
}

// manageChangeRelation godoc
//
//	@Summary		Change an entry's role
//	@Description	Gives an entry of a group the caller maintains another relation in one step, keeping its expiry; the history records one change. If the member holds the new relation already, the two entries become one, valid as long as the longer of them.
//	@Tags			manage
//	@Accept			json
//	@Security		Bearer
//	@Param			group	path	string					true	"Group id"
//	@Param			body	body	changeRelationRequest	true	"The entry and its new relation"
//	@Success		204		"Changed"
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		403		{object}	map[string]any	"Not maintained by the caller"
//	@Failure		404		{object}	map[string]any	"No such entry"
//	@ID				manageChangeRelation
//	@Router			/v1/manage/groups/{group}/members [patch]
func manageChangeRelation(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req changeRelationRequest
		if !bindJSON(c, &req) {
			return
		}
		if err := svc.ChangeRelation(c.Request.Context(), callerOf(c), c.Param("group"), req.Member, req.Relation, req.NewRelation); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// manageEffectiveMembers godoc
//
//	@Summary		Everyone the group counts
//	@Description	The people a group counts in the end (one the caller maintains, or for an admin any group): sub-groups resolved, expired entries left out. People who are members only through a pattern are not listed.
//	@Tags			manage
//	@Produce		json
//	@Security		Bearer
//	@Param			group	path		string	true	"Group id"
//	@Success		200		{array}		string	"user: tokens"
//	@Failure		403		{object}	map[string]any	"Not maintained by the caller"
//	@ID				manageEffectiveMembers
//	@Router			/v1/manage/groups/{group}/effective [get]
func manageEffectiveMembers(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		members, err := svc.EffectiveMembers(c.Request.Context(), callerOf(c), c.Param("group"))
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, members)
	}
}

type addManagerRequest struct {
	// Manager is user:<email>, group:<id> or group:<id>#<relation>.
	Manager string `json:"manager" binding:"required"`
}

// manageAddManager godoc
//
//	@Summary		Add a manager
//	@Description	Lets a person or a group (optionally one relation of it) maintain a group the caller maintains. Maintaining a group is not being in it: a manager gains none of the group's rights.
//	@Tags			manage
//	@Accept			json
//	@Security		Bearer
//	@Param			group	path	string				true	"Group id"
//	@Param			body	body	addManagerRequest	true	"The manager"
//	@Success		204		"Added"
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		403		{object}	map[string]any	"Not maintained by the caller"
//	@Failure		404		{object}	map[string]any	"The group named as manager does not exist"
//	@ID				manageAddManager
//	@Router			/v1/manage/groups/{group}/managers [post]
func manageAddManager(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req addManagerRequest
		if !bindJSON(c, &req) {
			return
		}
		if err := svc.AddManager(c.Request.Context(), callerOf(c), c.Param("group"), req.Manager); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// manageRemoveManager godoc
//
//	@Summary		Remove a manager
//	@Description	Takes the right to maintain a group the caller maintains away from a token.
//	@Tags			manage
//	@Security		Bearer
//	@Param			group	path	string	true	"Group id"
//	@Param			manager	query	string	true	"Manager token"
//	@Success		204		"Removed"
//	@Failure		403		{object}	map[string]any	"Not maintained by the caller"
//	@ID				manageRemoveManager
//	@Router			/v1/manage/groups/{group}/managers [delete]
func manageRemoveManager(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := svc.RemoveManager(c.Request.Context(), callerOf(c), c.Param("group"), c.Query("manager")); err != nil {
			respondError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// manageHistory godoc
//
//	@Summary		History of a group
//	@Description	The most recent changes of a group (one the caller maintains, or for an admin any group), newest first: who added or removed what, and for whom an admin acted.
//	@Tags			manage
//	@Produce		json
//	@Security		Bearer
//	@Param			group	path		string	true	"Group id"
//	@Param			limit	query		int		false	"Maximum entries"	default(100)
//	@Success		200		{array}		common.Change
//	@Failure		403		{object}	map[string]any	"Not maintained by the caller"
//	@ID				manageHistory
//	@Router			/v1/manage/groups/{group}/history [get]
func manageHistory(svc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := 100
		if n, err := strconv.Atoi(c.Query("limit")); err == nil && n > 0 && n <= 1000 {
			limit = n
		}
		changes, err := svc.History(c.Request.Context(), callerOf(c), c.Param("group"), limit)
		if err != nil {
			respondError(c, err)
			return
		}
		if changes == nil {
			changes = []common.Change{}
		}
		c.JSON(http.StatusOK, changes)
	}
}
