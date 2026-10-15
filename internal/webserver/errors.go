package webserver

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/pfisterer/role-provider-service/internal/common"
	"github.com/pfisterer/role-provider-service/internal/groupmgmt"
	"gorm.io/gorm"
)

const (
	// maxJSONBody bounds every request body on /v1 except an upload.
	maxJSONBody = 1 << 20
	// maxUploadBody bounds the CSV/LDIF file sent to /v1/sync/sources/:id/upload.
	maxUploadBody = 10 << 20
)

func respondError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, groupmgmt.ErrNotFound) || errors.Is(err, gorm.ErrRecordNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, groupmgmt.ErrAlreadyExists) || errors.Is(err, groupmgmt.ErrOwnedBySource):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	case errors.Is(err, groupmgmt.ErrInvalidToken) || errors.Is(err, groupmgmt.ErrInvalidRelation) || errors.Is(err, common.ErrInvalidID):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	default:
		internalError(c, err)
	}
}

// internalError answers 500 without the error's text, which may be a raw database message, and hands the error to the request log (see accessLoggingMiddleware).
func internalError(c *gin.Context, err error) {
	_ = c.Error(err)
	c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
}

// bindJSON decodes the request body into dst and answers the request itself when that fails: 413 for a body over the limit, 400 otherwise.
func bindJSON(c *gin.Context, dst any) bool {
	err := c.ShouldBindJSON(dst)
	if err == nil {
		return true
	}
	if tooLarge(c, err) {
		return false
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	return false
}

// tooLarge answers 413 when err comes from the body limit, and reports whether it did.
func tooLarge(c *gin.Context, err error) bool {
	var maxErr *http.MaxBytesError
	if !errors.As(err, &maxErr) {
		return false
	}
	c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": fmt.Sprintf("request body exceeds %d bytes", maxErr.Limit)})
	return true
}

// limitRequestBody caps how much of a request body a handler can read: maxUploadBody for a file upload, maxJSONBody for everything else.
func limitRequestBody() gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := int64(maxJSONBody)
		if c.FullPath() == uploadRoute {
			limit = maxUploadBody
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
