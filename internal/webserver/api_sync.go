package webserver

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/storage"
	syncp "github.com/pfisterer/role-provider-service/internal/sync"
)

func registerSyncRoutes(rg *gin.RouterGroup, store storage.Store, engine *syncp.Engine, scheduler *syncp.Scheduler) {
	s := rg.Group("/sync/sources")
	s.GET("", listSources(store))
	s.POST("", createSource(store, scheduler))
	s.GET("/:id", getSource(store))
	s.PUT("/:id", updateSource(store, scheduler))
	s.DELETE("/:id", deleteSource(store, scheduler))
	s.POST("/:id/upload", uploadAndSync(engine))
	s.POST("/:id/trigger", triggerSync(engine))
	s.GET("/:id/log", getSyncLog(store))
}

// listSources godoc
//
//	@Summary		List sync sources
//	@Description	Returns all configured sync sources (CSV/LDIF files).
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
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
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
}

// createSource godoc
//
//	@Summary		Create sync source
//	@Description	Registers a new sync source. Type must be "csv" or "ldif". An optional cron schedule triggers automatic syncs.
//	@Tags			sync
//	@Accept			json
//	@Produce		json
//	@Security		Bearer
//	@Param			body	body		createSourceRequest	true	"Source definition"
//	@Success		201		{object}	common.Source
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
//	@Failure		500		{object}	map[string]any	"Internal server error"
//	@ID				createSyncSource
//	@Router			/v1/sync/sources [post]
func createSource(store storage.Store, scheduler *syncp.Scheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createSourceRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		typ := strings.ToLower(req.Type)
		if typ != common.SourceTypeCSV && typ != common.SourceTypeLDIF {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("type must be '%s' or '%s'", common.SourceTypeCSV, common.SourceTypeLDIF)})
			return
		}
		src := &common.Source{
			ID:            uuid.New(),
			Name:          req.Name,
			Type:          typ,
			Schedule:      req.Schedule,
			DNEmailRegexp: req.DNEmailRegexp,
			FilePath:      req.FilePath,
		}
		if err := store.CreateSource(c.Request.Context(), src); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if src.Schedule != "" {
			if err := scheduler.Register(src.ID, src.Schedule); err != nil {
				c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid schedule: %s", err)})
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
}

// updateSource godoc
//
//	@Summary		Update sync source
//	@Description	Updates name, schedule, DN regexp, or file path of an existing sync source.
//	@Tags			sync
//	@Accept			json
//	@Produce		json
//	@Security		Bearer
//	@Param			id		path		string				true	"Source UUID"
//	@Param			body	body		updateSourceRequest	true	"Fields to update"
//	@Success		200		{object}	common.Source
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
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
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		if err := store.UpdateSource(c.Request.Context(), id, req.Name, req.Schedule, req.DNEmailRegexp, req.FilePath); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if req.Schedule != "" {
			_ = scheduler.Register(id, req.Schedule)
		} else {
			scheduler.Unregister(id)
		}
		src, err := store.GetSource(c.Request.Context(), id)
		if err != nil {
			respondError(c, err)
			return
		}
		c.JSON(http.StatusOK, src)
	}
}

// deleteSource godoc
//
//	@Summary		Delete sync source
//	@Description	Deletes a sync source and removes all tuples it owned. Groups created by this source are orphaned (not deleted).
//	@Tags			sync
//	@Security		Bearer
//	@Param			id	path	string	true	"Source UUID"
//	@Success		204	"No content"
//	@Failure		400	{object}	map[string]any	"Invalid UUID"
//	@Failure		401	{object}	map[string]any	"Unauthorized"
//	@Failure		500	{object}	map[string]any	"Internal server error"
//	@ID				deleteSyncSource
//	@Router			/v1/sync/sources/{id} [delete]
func deleteSource(store storage.Store, scheduler *syncp.Scheduler) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, err := uuid.Parse(c.Param("id"))
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid source id"})
			return
		}
		scheduler.Unregister(id)
		if err := store.DeleteSource(c.Request.Context(), id); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.Status(http.StatusNoContent)
	}
}

// uploadAndSync godoc
//
//	@Summary		Upload file and sync
//	@Description	Uploads a CSV or LDIF file and immediately runs a sync for the given source. Replaces all tuples owned by this source.
//	@Tags			sync
//	@Accept			multipart/form-data
//	@Produce		json
//	@Security		Bearer
//	@Param			id		path		string	true	"Source UUID"
//	@Param			file	formData	file	true	"CSV or LDIF file to sync"
//	@Success		200		{object}	map[string]any	"Sync result"
//	@Failure		400		{object}	map[string]any	"Bad request"
//	@Failure		401		{object}	map[string]any	"Unauthorized"
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
			c.JSON(http.StatusBadRequest, gin.H{"error": "missing 'file' in multipart form"})
			return
		}
		defer file.Close()

		content, err := io.ReadAll(file)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "failed to read uploaded file"})
			return
		}

		if err := engine.RunSync(c.Request.Context(), id, content); err != nil {
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
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
			c.JSON(http.StatusUnprocessableEntity, gin.H{"error": err.Error()})
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
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, logs)
	}
}
