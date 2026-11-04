package webserver

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
	"github.com/pfisterer/role-provider-service/internal/storage"
	syncp "github.com/pfisterer/role-provider-service/internal/sync"
)

// uploadRoute is the one route whose body may be larger than maxJSONBody.
const uploadRoute = "/v1/sync/sources/:id/upload"

func registerSyncRoutes(rg *gin.RouterGroup, store storage.Store, engine *syncp.Engine, scheduler *syncp.Scheduler, groupSvc *groupmgmt.Service, write, upload gin.HandlerFunc) {
	s := rg.Group("/sync/sources")
	s.GET("", listSources(store))
	s.POST("", write, createSource(store, scheduler))
	s.GET("/:id", getSource(store))
	s.PUT("/:id", write, updateSource(store, scheduler))
	s.DELETE("/:id", write, deleteSource(store, scheduler, groupSvc))
	s.POST("/:id/upload", upload, uploadAndSync(engine))
	s.POST("/:id/trigger", write, triggerSync(engine))
	s.GET("/:id/log", getSyncLog(store))
}

// listSources godoc
//
//	@Summary		List sync sources
//	@Description	Returns all sources: the local one (type "local", maintained through the group API and the self-service UI), the Keycloak source if configured, and the CSV/LDIF sources.
//	@Tags			sync
//	@Produce		json
//	@Security		Bearer
//	@Success		200	{array}		common.Source
//	@Failure		401	{object}	map[string]any	"Unauthorized"
//	@Failure		500	{object}	map[string]any	"Internal server error"
//	@ID				listSyncSources
//	@Router			/v1/sync/sources [get]
func listSources(store storage.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		sources, err := store.ListSources(c.Request.Context())
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, sources)
	}
}

type createSourceRequest struct {
	Name          string `json:"name" binding:"required"`
	Type          string `json:"type" binding:"required"`
	Schedule      string `json:"schedule"`
	DNEmailRegexp string `json:"dn_email_regexp"`
	FilePath      string `json:"file_path"`
	// GroupRelationRegexp (LDIF only) maps a group CN onto a relation of another
	// group via the named captures "group" and "relation".
	GroupRelationRegexp string `json:"group_relation_regexp"`
	// ReservedNames are globs of the group ids only this source may create ("*" for any run of characters).
	ReservedNames []string `json:"reserved_names"`
}

// createSource godoc
//
//	@Summary		Create sync source
//	@Description	Registers a new sync source. Type must be "csv" or "ldif". An optional cron schedule triggers automatic syncs. Reserved names that match a group of another source are refused (409).
//	@Tags			sync
//	@Accept			json
//	@Produce		json
//	@Security		Bearer
//	@Param			body	body		createSourceRequest	true	"Source definition"
//	@Success		201		{object}	common.Source
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		409		{object}	map[string]any	"Reserved names taken"
//	@Failure		500		{object}	map[string]any	"Internal server error"
//	@ID				createSyncSource
//	@Router			/v1/sync/sources [post]
func createSource(store storage.Store, scheduler *syncp.Scheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createSourceRequest
		if !bindJSON(c, &req) {
			return
		}
		typ := strings.ToLower(req.Type)
		if typ != common.SourceTypeCSV && typ != common.SourceTypeLDIF {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("type must be '%s' or '%s'", common.SourceTypeCSV, common.SourceTypeLDIF)})
			return
		}
		if req.Schedule != "" {
			if err := syncp.ValidateSchedule(req.Schedule); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		reserved, ok := reservedNames(c, store, uuid.Nil, req.ReservedNames)
		if !ok {
			return
		}
		src := &common.Source{
			ID:                  uuid.New(),
			Name:                req.Name,
			Type:                typ,
			Schedule:            req.Schedule,
			DNEmailRegexp:       req.DNEmailRegexp,
			GroupRelationRegexp: req.GroupRelationRegexp,
			FilePath:            req.FilePath,
			ReservedNames:       reserved,
		}
		if err := store.CreateSource(c.Request.Context(), src); err != nil {
			internalError(c, err)
			return
		}
		if src.Schedule != "" {
			if err := scheduler.Register(src.ID, src.Schedule); err != nil {
				internalError(c, fmt.Errorf("register schedule of source %s: %w", src.ID, err))
				return
			}
		}
		c.JSON(http.StatusCreated, src)
	}
}

// getSource godoc
//
//	@Summary		Get sync source
//	@Description	Returns a single sync source by UUID.
//	@Tags			sync
//	@Produce		json
//	@Security		Bearer
//	@Param			id	path		string	true	"Source UUID"
//	@Success		200	{object}	common.Source
//	@Failure		400	{object}	map[string]any	"Invalid UUID"
//	@Failure		401	{object}	map[string]any	"Unauthorized"
//	@Failure		404	{object}	map[string]any	"Not found"
//	@ID				getSyncSource
//	@Router			/v1/sync/sources/{id} [get]
func getSource(store storage.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		src, err := store.GetSource(c.Request.Context(), id)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, src)
	}
}

type updateSourceRequest struct {
	Name          string `json:"name"`
	Schedule      string `json:"schedule"`
	DNEmailRegexp string `json:"dn_email_regexp"`
	FilePath      string `json:"file_path"`
	// GroupRelationRegexp (LDIF only) maps a group CN onto a relation of another
	// group via the named captures "group" and "relation".
	GroupRelationRegexp string `json:"group_relation_regexp"`
	// ReservedNames are globs of the group ids only this source may create ("*" for any run of characters).
	ReservedNames []string `json:"reserved_names"`
}

// updateSource godoc
//
//	@Summary		Update sync source
//	@Description	Replaces name, schedule, regexps, file path and reserved names of a sync source. The local source is not changed here (409).
//	@Tags			sync
//	@Accept			json
//	@Produce		json
//	@Security		Bearer
//	@Param			id		path		string				true	"Source UUID"
//	@Param			body	body		updateSourceRequest	true	"Fields to update"
//	@Success		200		{object}	common.Source
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		404		{object}	map[string]any	"Not found"
//	@Failure		409		{object}	map[string]any	"The local source, or reserved names taken"
//	@Failure		500		{object}	map[string]any	"Internal server error"
//	@ID				updateSyncSource
//	@Router			/v1/sync/sources/{id} [put]
func updateSource(store storage.Store, scheduler *syncp.Scheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		var req updateSourceRequest
		if !bindJSON(c, &req) {
			return
		}
		if req.Schedule != "" {
			if err := syncp.ValidateSchedule(req.Schedule); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
				return
			}
		}
		src, ok := changeableSource(c, store, id)
		if !ok {
			return
		}
		reserved, ok := reservedNames(c, store, id, req.ReservedNames)
		if !ok {
			return
		}
		src.Name, src.Schedule, src.DNEmailRegexp, src.GroupRelationRegexp, src.FilePath, src.ReservedNames =
			req.Name, req.Schedule, req.DNEmailRegexp, req.GroupRelationRegexp, req.FilePath, reserved
		if err := store.UpdateSource(c.Request.Context(), src); err != nil {
			internalError(c, err)
			return
		}
		if src, err = store.GetSource(c.Request.Context(), id); err != nil {
			respondError(c, err)
			return
		}
		if src.Schedule != "" {
			if err := scheduler.Register(id, src.Schedule); err != nil {
				internalError(c, fmt.Errorf("register schedule of source %s: %w", id, err))
				return
			}
		} else {
			scheduler.Unregister(id)
		}
		c.JSON(http.StatusOK, src)
	}
}

// deleteSource godoc
//
//	@Summary		Delete sync source
//	@Description	Deletes a sync source and removes all tuples it owned. Its groups stay, empty, and pass to the local source, where they can be filled again. The local source cannot be deleted (409).
//	@Tags			sync
//	@Security		Bearer
//	@Param			id	path	string	true	"Source UUID"
//	@Success		204	"No content"
//	@Failure		400	{object}	map[string]any	"Invalid UUID"
//	@Failure		401	{object}	map[string]any	"Unauthorized"
//	@Failure		404	{object}	map[string]any	"Not found"
//	@Failure		409	{object}	map[string]any	"The local source"
//	@Failure		500	{object}	map[string]any	"Internal server error"
//	@ID				deleteSyncSource
//	@Router			/v1/sync/sources/{id} [delete]
func deleteSource(store storage.Store, scheduler *syncp.Scheduler, groupSvc *groupmgmt.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		src, ok := changeableSource(c, store, id)
		if !ok {
			return
		}
		scheduler.Unregister(id)
		if err := store.DeleteSource(c.Request.Context(), id); err != nil {
			internalError(c, err)
			return
		}
		// Every group belongs to a source; the deleted one's pass to the local source rather than to nobody.
		if _, err := groupSvc.Adopt(c.Request.Context(), groupmgmt.APITokenCaller(), nil, fmt.Sprintf("source %q was deleted", src.Name)); err != nil {
			internalError(c, err)
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// changeableSource loads a source for a change through this API and answers the request itself when there is none or it is the local source, which is maintained through the group API instead.
func changeableSource(c *gin.Context, store storage.Store, id uuid.UUID) (*common.Source, bool) {
	src, err := store.GetSource(c.Request.Context(), id)
	if err != nil {
		respondError(c, err)
		return nil, false
	}
	if src.Type == common.SourceTypeLocal {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("source %q is the local source; it is maintained through the group API", src.Name)})
		return nil, false
	}
	return src, true
}

// reservedNames normalizes and checks the names a source reserves and answers the request itself when they are malformed (400) or taken (409).
func reservedNames(c *gin.Context, store storage.Store, sourceID uuid.UUID, globs []string) ([]string, bool) {
	reserved, err := syncp.NormalizeReservedNames(globs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return nil, false
	}
	if err := syncp.CheckReservation(c.Request.Context(), store, sourceID, reserved); err != nil {
		if errors.Is(err, syncp.ErrReservationConflict) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		} else {
			internalError(c, err)
		}
		return nil, false
	}
	return reserved, true
}

// uploadAndSync godoc
//
//	@Summary		Upload file and sync
//	@Description	Uploads a CSV or LDIF file and immediately runs a sync for the given source. Replaces all tuples owned by this source. Needs a write token, or the upload token issued for this source (SOURCE_UPLOAD_TOKENS).
//	@Tags			sync
//	@Accept			multipart/form-data
//	@Produce		json
//	@Security		Bearer
//	@Param			id		path		string	true	"Source UUID"
//	@Param			file	formData	file	true	"CSV or LDIF file to sync"
//	@Success		200		{object}	map[string]any	"Sync result"
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		403		{object}	map[string]any	"Read token, or upload token of another source"
//	@Failure		422		{object}	map[string]any	"Sync error (parse or apply failure)"
//	@ID				uploadAndSync
//	@Router			/v1/sync/sources/{id}/upload [post]
func uploadAndSync(engine *syncp.Engine) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		file, _, err := c.Request.FormFile("file")
		if err != nil {
			if tooLarge(c, err) {
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing 'file' in multipart form"})
			return
		}
		defer file.Close()

		content, err := io.ReadAll(file)
		if err != nil {
			internalError(c, fmt.Errorf("read uploaded file: %w", err))
			return
		}

		if err := engine.RunSync(c.Request.Context(), id, content); err != nil {
			respondSyncError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "sync completed"})
	}
}

// triggerSync godoc
//
//	@Summary		Trigger sync from file path
//	@Description	Triggers a sync for a source that has a FilePath configured, reading from that path on disk.
//	@Tags			sync
//	@Produce		json
//	@Security		Bearer
//	@Param			id	path		string	true	"Source UUID"
//	@Success		200	{object}	map[string]any	"Sync result"
//	@Failure		400	{object}	map[string]any	"Invalid UUID"
//	@Failure		401	{object}	map[string]any	"Unauthorized"
//	@Failure		422	{object}	map[string]any	"Sync error"
//	@ID				triggerSync
//	@Router			/v1/sync/sources/{id}/trigger [post]
func triggerSync(engine *syncp.Engine) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		if err := engine.RunSync(c.Request.Context(), id, nil); err != nil {
			respondSyncError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"status": "sync completed"})
	}
}

// getSyncLog godoc
//
//	@Summary		Get sync log
//	@Description	Returns the most recent sync log entries for a source (default: last 20).
//	@Tags			sync
//	@Produce		json
//	@Security		Bearer
//	@Param			id	path		string	true	"Source UUID"
//	@Success		200	{array}		common.SyncLog
//	@Failure		400	{object}	map[string]any	"Invalid UUID"
//	@Failure		401	{object}	map[string]any	"Unauthorized"
//	@Failure		500	{object}	map[string]any	"Internal server error"
//	@ID				getSyncLog
//	@Router			/v1/sync/sources/{id}/log [get]
func getSyncLog(store storage.Store) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		limit := 20
		logs, err := store.ListSyncLogs(c.Request.Context(), id, limit)
		if err != nil {
			internalError(c, err)
			return
		}
		c.JSON(http.StatusOK, logs)
	}
}

// respondSyncError answers 422 with the message when the source's data was refused, and treats everything else (an unknown source, a database failure) like any other error.
func respondSyncError(c *gin.Context, err error) {
	var importErr *syncp.ImportError
	if errors.As(err, &importErr) {
		c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
		return
	}
	respondError(c, err)
}
