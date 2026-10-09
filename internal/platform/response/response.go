// Package response holds the HTTP response envelope. The shape is kept
// identical to the one the legacy Express services returned, so existing
// front-end clients do not have to change when they are pointed at a service.
package response

import (
	"net/http"
	"reflect"

	"github.com/gin-gonic/gin"
)

// Meta is the pagination block of a list response.
type Meta struct {
	Page       int   `json:"page"`
	Limit      int   `json:"limit"`
	TotalRows  int64 `json:"totalRows"`
	TotalPages int   `json:"totalPages"`
}

// Envelope is the success shape every endpoint returns. Exported because
// the @Success annotations name it, and swag refuses a document that names
// a type the code does not have.
type Envelope struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data"`
	Meta    *Meta       `json:"meta,omitempty"`
}

// Error is the failure shape, named by the @Failure annotations for the
// same reason.
type Error struct {
	Success bool        `json:"success"`
	Message string      `json:"message"`
	Errors  interface{} `json:"errors,omitempty"`
}

func OK(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Envelope{Success: true, Data: data})
}

func OKWithMessage(c *gin.Context, message string, data interface{}) {
	c.JSON(http.StatusOK, Envelope{Success: true, Message: message, Data: data})
}

func Created(c *gin.Context, data interface{}) {
	c.JSON(http.StatusCreated, Envelope{Success: true, Data: data})
}

func Paginated(c *gin.Context, data interface{}, meta *Meta) {
	c.JSON(http.StatusOK, Envelope{Success: true, Data: emptyList(data), Meta: meta})
}

// emptyList turns a nil slice into an empty one so a page with no rows
// marshals as [] rather than null.
//
// A Go nil slice and an empty slice are the same thing to Go and different
// things to every client: `data.map(...)` on null throws, so an empty result
// crashes the page that renders it while a populated result works. The fix
// belongs here rather than in each handler, because the ones that forget are
// exactly the endpoints nobody has yet seen return nothing.
func emptyList(data interface{}) interface{} {
	v := reflect.ValueOf(data)
	if v.Kind() == reflect.Slice && v.IsNil() {
		return reflect.MakeSlice(v.Type(), 0, 0).Interface()
	}
	return data
}

func BadRequest(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, Error{Message: message})
}

func ValidationFailed(c *gin.Context, message string, errs interface{}) {
	c.AbortWithStatusJSON(http.StatusUnprocessableEntity, Error{Message: message, Errors: errs})
}

func Unauthorized(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusUnauthorized, Error{Message: message})
}

func Forbidden(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusForbidden, Error{Message: message})
}

func NotFound(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusNotFound, Error{Message: message})
}

func Conflict(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusConflict, Error{Message: message})
}

// InternalError reports a server fault. The message is intended for logs and
// operators; callers should not pass raw driver errors through to clients.
func InternalError(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusInternalServerError, Error{Message: message})
}
