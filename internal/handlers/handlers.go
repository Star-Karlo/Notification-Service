// Package handlers exposes the notification service over HTTP.
//
// The HTTP surface is small on purpose. Most traffic arrives over gRPC from
// other services; these endpoints serve the user's own inbox, and the WhatsApp
// webhook.
package handlers

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/karlo/notification-service/internal/config"
	"github.com/karlo/notification-service/internal/models"
	"github.com/karlo/notification-service/internal/platform/authctx"
	"github.com/karlo/notification-service/internal/platform/query"
	"github.com/karlo/notification-service/internal/platform/response"
	"github.com/karlo/notification-service/internal/repository"
	"github.com/karlo/notification-service/internal/services"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

// InboxHandler serves a user's own notifications.
type InboxHandler struct {
	dispatcher *services.Dispatcher
}

func NewInboxHandler(dispatcher *services.Dispatcher) *InboxHandler {
	return &InboxHandler{dispatcher: dispatcher}
}

// List pages the caller's inbox.
//
// The user id comes from the token, never from a query parameter, so one user
// cannot read another's notifications.
//
// @Summary  List notifications
// @Tags     Notifications
// @Security BearerAuth
// @Success  200 {object} response.Meta
// @Router   /notifications [get]
func (h *InboxHandler) List(c *gin.Context) {
	userID, ok := callerID(c)
	if !ok {
		return
	}

	params := query.Parse(
		c.DefaultQuery("page", "0"),
		c.DefaultQuery("pageSize", "20"),
		c.Query("filtered"),
		c.Query("sorted"),
		"",
		repository.NotificationFields(),
	)

	unreadOnly := c.Query("unread") == "true"

	items, total, err := h.dispatcher.ListForUser(c.Request.Context(), userID, unreadOnly, params)
	if err != nil {
		response.InternalError(c, "Failed to load notifications")
		return
	}

	response.Paginated(c, items, &response.Meta{
		Page:       params.Page,
		Limit:      params.PageSize,
		TotalRows:  total,
		TotalPages: params.TotalPages(total),
	})
}

// Unread returns the badge count.
//
// @Summary  Count unread notifications
// @Tags     Notifications
// @Security BearerAuth
// @Success  200 {object} object
// @Router   /notifications/unread [get]
func (h *InboxHandler) Unread(c *gin.Context) {
	userID, ok := callerID(c)
	if !ok {
		return
	}

	count, err := h.dispatcher.CountUnread(c.Request.Context(), userID)
	if err != nil {
		response.InternalError(c, "Failed to count notifications")
		return
	}

	response.OK(c, gin.H{"count": count})
}

// MarkRead marks notifications as read.
//
// @Summary  Mark notifications read
// @Tags     Notifications
// @Security BearerAuth
// @Success  200 {object} object
// @Router   /notifications/read [post]
func (h *InboxHandler) MarkRead(c *gin.Context) {
	userID, ok := callerID(c)
	if !ok {
		return
	}

	var body struct {
		IDs []string `json:"ids"`
		All bool     `json:"all"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if !body.All && len(body.IDs) == 0 {
		response.BadRequest(c, `supply "ids" or set "all" to true`)
		return
	}

	ids := make([]primitive.ObjectID, 0, len(body.IDs))
	for _, raw := range body.IDs {
		id, err := primitive.ObjectIDFromHex(raw)
		if err != nil {
			response.BadRequest(c, "Invalid notification id: "+raw)
			return
		}
		ids = append(ids, id)
	}

	updated, err := h.dispatcher.MarkRead(c.Request.Context(), userID, ids, body.All)
	if err != nil {
		response.InternalError(c, "Failed to update notifications")
		return
	}

	response.OKWithMessage(c, "Notifications updated", gin.H{"updated": updated})
}

// OTPHandler serves the one-time password endpoints.
type OTPHandler struct {
	otp *services.OTPService
}

func NewOTPHandler(otp *services.OTPService) *OTPHandler {
	return &OTPHandler{otp: otp}
}

// Send issues a code.
//
// @Summary  Send an OTP
// @Tags     OTP
// @Success  200 {object} object
// @Router   /otp/send [post]
func (h *OTPHandler) Send(c *gin.Context) {
	var body struct {
		PhoneNumber string `json:"phoneNumber" binding:"required"`
		Purpose     string `json:"purpose"`
		Language    string `json:"language"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	result, err := h.otp.Send(c.Request.Context(), body.PhoneNumber, body.Purpose, body.Language)
	if err != nil {
		if errors.Is(err, services.ErrOTPRateLimited) && result != nil {
			c.Header("Retry-After", itoa(result.RetryAfterSeconds))
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"success":           false,
				"message":           "Please wait before requesting another code.",
				"retryAfterSeconds": result.RetryAfterSeconds,
			})
			return
		}
		if errors.Is(err, services.ErrValidation) {
			response.BadRequest(c, err.Error())
			return
		}
		response.InternalError(c, "Failed to send the code")
		return
	}

	response.OKWithMessage(c, "Code sent", gin.H{"expiresAt": result.ExpiresAt})
}

// Verify checks a code.
//
// @Summary  Verify an OTP
// @Tags     OTP
// @Success  200 {object} object
// @Router   /otp/verify [post]
func (h *OTPHandler) Verify(c *gin.Context) {
	var body struct {
		PhoneNumber string `json:"phoneNumber" binding:"required"`
		Code        string `json:"code" binding:"required"`
		Purpose     string `json:"purpose"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		response.BadRequest(c, err.Error())
		return
	}

	result, err := h.otp.Verify(c.Request.Context(), body.PhoneNumber, body.Code, body.Purpose)
	if err != nil {
		if errors.Is(err, services.ErrValidation) {
			response.BadRequest(c, err.Error())
			return
		}
		response.InternalError(c, "Failed to verify the code")
		return
	}

	if !result.Verified {
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"success":           false,
			"message":           result.Reason,
			"attemptsRemaining": result.AttemptsRemaining,
		})
		return
	}

	response.OKWithMessage(c, "Verified", nil)
}

// WebhookHandler receives provider callbacks.
type WebhookHandler struct {
	inbound *repository.InboundRepository
	cfg     *config.Config
}

func NewWebhookHandler(inbound *repository.InboundRepository, cfg *config.Config) *WebhookHandler {
	return &WebhookHandler{inbound: inbound, cfg: cfg}
}

// Verify answers Meta's subscription challenge.
//
// @Summary  Verify the WhatsApp webhook
// @Tags     Webhooks
// @Router   /webhooks/whatsapp [get]
func (h *WebhookHandler) Verify(c *gin.Context) {
	mode := c.Query("hub.mode")
	token := c.Query("hub.verify_token")
	challenge := c.Query("hub.challenge")

	// The comparison is constant-time: a timing-variable one would let the
	// verify token be recovered a character at a time.
	if mode == "subscribe" && h.cfg.WhatsApp.VerifyToken != "" &&
		secureEqual(token, h.cfg.WhatsApp.VerifyToken) {
		c.String(http.StatusOK, challenge)
		return
	}

	c.String(http.StatusForbidden, "Forbidden")
}

// Receive accepts inbound WhatsApp messages.
//
// The payload is persisted before the handler returns. The legacy version
// logged the body and spawned a goroutine that did nothing, so every inbound
// customer message was lost. Storing first means a processing failure can be
// retried rather than losing the message.
//
// @Summary  Receive WhatsApp events
// @Tags     Webhooks
// @Router   /webhooks/whatsapp [post]
func (h *WebhookHandler) Receive(c *gin.Context) {
	var payload map[string]interface{}
	if err := c.ShouldBindJSON(&payload); err != nil {
		// Meta retries on a non-2xx, and a body this service cannot parse will
		// not become parseable on retry. Acknowledge and record nothing.
		c.JSON(http.StatusOK, gin.H{"status": "ignored"})
		return
	}

	messages := extractWhatsAppMessages(payload)
	stored := 0

	for _, msg := range messages {
		inserted, err := h.inbound.Store(c.Request.Context(), &models.InboundMessage{
			Provider:   "whatsapp",
			ProviderID: msg.ID,
			FromPhone:  msg.From,
			Text:       msg.Text,
			Payload:    msg.Raw,
		})
		if err != nil {
			// Returning non-2xx asks Meta to redeliver, which is what we want
			// when the store failed.
			response.InternalError(c, "Failed to record the message")
			return
		}
		if inserted {
			stored++
		}
	}

	c.JSON(http.StatusOK, gin.H{"status": "received", "stored": stored})
}

func callerID(c *gin.Context) (string, bool) {
	principal, ok := authctx.Gin(c)
	if !ok {
		response.Unauthorized(c, "No token provided.")
		return "", false
	}
	return principal.UserID, true
}
