// Package grpcserver implements the NotificationService contract.
package grpcserver

import (
	"context"
	"errors"

	"github.com/karlo/notification-service/internal/channels"
	"github.com/karlo/notification-service/internal/models"
	notificationv1 "github.com/karlo/notification-service/internal/platform/genproto/karlo/notification/v1"
	"github.com/karlo/notification-service/internal/platform/query"
	"github.com/karlo/notification-service/internal/platform/safeconv"
	"github.com/karlo/notification-service/internal/services"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/structpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type Server struct {
	notificationv1.UnimplementedNotificationServiceServer

	dispatcher *services.Dispatcher
	otp        *services.OTPService
	email      *channels.Email
}

func New(dispatcher *services.Dispatcher, otp *services.OTPService, email *channels.Email) *Server {
	return &Server{dispatcher: dispatcher, otp: otp, email: email}
}

// Notify is the RPC the ~117 legacy Notification.create sites map onto.
func (s *Server) Notify(ctx context.Context, req *notificationv1.NotifyRequest) (*notificationv1.NotifyResponse, error) {
	result, err := s.dispatcher.Notify(ctx, toNotifyRequest(req))
	if err != nil {
		return nil, mapError(err)
	}

	return &notificationv1.NotifyResponse{
		NotificationId: result.NotificationID,
		Delivered:      toProtoResults(result.Results),
		Deduplicated:   result.Deduplicated,
	}, nil
}

// NotifyBatch delivers several unrelated notifications in one round trip.
//
// One failure does not abandon the others: a bulk order import that raises
// forty notifications should not lose thirty-nine because one had a bad
// audience. Failures are reported per item.
func (s *Server) NotifyBatch(ctx context.Context, req *notificationv1.NotifyBatchRequest) (*notificationv1.NotifyBatchResponse, error) {
	out := make([]*notificationv1.NotifyResponse, 0, len(req.GetNotifications()))

	for _, item := range req.GetNotifications() {
		result, err := s.dispatcher.Notify(ctx, toNotifyRequest(item))
		if err != nil {
			out = append(out, &notificationv1.NotifyResponse{
				Delivered: []*notificationv1.ChannelResult{{
					Success: false,
					Error:   err.Error(),
				}},
			})
			continue
		}
		out = append(out, &notificationv1.NotifyResponse{
			NotificationId: result.NotificationID,
			Delivered:      toProtoResults(result.Results),
			Deduplicated:   result.Deduplicated,
		})
	}

	return &notificationv1.NotifyBatchResponse{Results: out}, nil
}

func (s *Server) ListNotifications(ctx context.Context, req *notificationv1.ListNotificationsRequest) (*notificationv1.ListNotificationsResponse, error) {
	if req.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	params := query.FromProto(req.GetQuery(), repositoryFields())

	items, total, err := s.dispatcher.ListForUser(ctx, req.GetUserId(), req.GetUnreadOnly(), params)
	if err != nil {
		return nil, mapError(err)
	}

	out := make([]*notificationv1.Notification, 0, len(items))
	for i := range items {
		out = append(out, toProtoNotification(&items[i]))
	}

	return &notificationv1.ListNotificationsResponse{
		Notifications: out,
		PageInfo:      params.PageInfo(total),
	}, nil
}

func (s *Server) CountUnread(ctx context.Context, req *notificationv1.CountUnreadRequest) (*notificationv1.CountUnreadResponse, error) {
	if req.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	count, err := s.dispatcher.CountUnread(ctx, req.GetUserId())
	if err != nil {
		return nil, mapError(err)
	}

	return &notificationv1.CountUnreadResponse{Count: count}, nil
}

func (s *Server) MarkRead(ctx context.Context, req *notificationv1.MarkReadRequest) (*notificationv1.MarkReadResponse, error) {
	if req.GetUserId() == "" {
		return nil, status.Error(codes.InvalidArgument, "user_id is required")
	}

	ids := make([]primitive.ObjectID, 0, len(req.GetNotificationIds()))
	for _, raw := range req.GetNotificationIds() {
		if id, err := primitive.ObjectIDFromHex(raw); err == nil {
			ids = append(ids, id)
		}
	}

	updated, err := s.dispatcher.MarkRead(ctx, req.GetUserId(), ids, req.GetAll())
	if err != nil {
		return nil, mapError(err)
	}

	return &notificationv1.MarkReadResponse{Updated: updated}, nil
}

func (s *Server) SendOtp(ctx context.Context, req *notificationv1.SendOtpRequest) (*notificationv1.SendOtpResponse, error) {
	result, err := s.otp.Send(ctx, req.GetPhoneNumber(), req.GetPurpose(), req.GetLanguage())
	if err != nil {
		if errors.Is(err, services.ErrOTPRateLimited) && result != nil {
			// A throttled request is a normal answer with a retry hint, not a
			// server error.
			return &notificationv1.SendOtpResponse{
				Sent:              false,
				RetryAfterSeconds: safeconv.NonNegativeInt32(result.RetryAfterSeconds),
			}, nil
		}
		return nil, mapError(err)
	}

	return &notificationv1.SendOtpResponse{
		Sent:      result.Sent,
		ExpiresAt: timestamppb.New(result.ExpiresAt),
	}, nil
}

func (s *Server) VerifyOtp(ctx context.Context, req *notificationv1.VerifyOtpRequest) (*notificationv1.VerifyOtpResponse, error) {
	result, err := s.otp.Verify(ctx, req.GetPhoneNumber(), req.GetCode(), req.GetPurpose())
	if err != nil {
		return nil, mapError(err)
	}

	return &notificationv1.VerifyOtpResponse{
		Verified:          result.Verified,
		Reason:            result.Reason,
		AttemptsRemaining: safeconv.NonNegativeInt32(result.AttemptsRemaining),
	}, nil
}

// SendDocumentEmail replaces the direct nodemailer calls that were scattered
// through the legacy order and shipment controllers.
func (s *Server) SendDocumentEmail(ctx context.Context, req *notificationv1.SendDocumentEmailRequest) (*notificationv1.SendDocumentEmailResponse, error) {
	if len(req.GetTo()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "at least one recipient is required")
	}
	if !s.email.Enabled() {
		return &notificationv1.SendDocumentEmailResponse{
			Sent:  false,
			Error: "email is not configured",
		}, nil
	}

	attachments := make([]channels.Attachment, 0, len(req.GetAttachments()))
	for _, a := range req.GetAttachments() {
		if len(a.GetContent()) == 0 {
			// A URL-only attachment is a reference to something already in
			// storage; the template links to it rather than inlining it.
			continue
		}
		attachments = append(attachments, channels.Attachment{
			Filename:    a.GetFilename(),
			ContentType: a.GetContentType(),
			Content:     a.GetContent(),
		})
	}

	subject, body := renderDocumentEmail(req.GetTemplate(), req.GetParams(), req.GetLanguage())

	err := s.email.SendWithAttachments(ctx, req.GetTo(), req.GetCc(), subject, body, attachments)
	if err != nil {
		return &notificationv1.SendDocumentEmailResponse{Sent: false, Error: err.Error()}, nil
	}

	return &notificationv1.SendDocumentEmailResponse{Sent: true}, nil
}

// RegisterDeviceToken is served by the authentication service, which owns the
// device register. Reporting Unimplemented is honest: silently succeeding would
// leave a caller believing their device was registered.
func (s *Server) RegisterDeviceToken(context.Context, *notificationv1.RegisterDeviceTokenRequest) (*notificationv1.RegisterDeviceTokenResponse, error) {
	return nil, status.Error(codes.Unimplemented,
		"device tokens are registered through the authentication service")
}

// ---------------------------------------------------------------------------
// Mapping
// ---------------------------------------------------------------------------

func toNotifyRequest(req *notificationv1.NotifyRequest) services.NotifyRequest {
	out := services.NotifyRequest{
		Event:          req.GetEvent(),
		Audience:       req.GetAudience(),
		SubjectID:      req.GetSubjectId(),
		SubjectType:    req.GetSubjectType(),
		ActorID:        req.GetActorId(),
		IdempotencyKey: req.GetIdempotencyKey(),
	}
	if params := req.GetParams(); params != nil {
		out.Params = params.AsMap()
	}
	for _, ch := range req.GetChannels() {
		if mapped, ok := channelFromProto(ch); ok {
			out.Channels = append(out.Channels, mapped)
		}
	}
	return out
}

func channelFromProto(ch notificationv1.Channel) (models.Channel, bool) {
	switch ch {
	case notificationv1.Channel_CHANNEL_IN_APP:
		return models.ChannelInApp, true
	case notificationv1.Channel_CHANNEL_PUSH:
		return models.ChannelPush, true
	case notificationv1.Channel_CHANNEL_EMAIL:
		return models.ChannelEmail, true
	case notificationv1.Channel_CHANNEL_WHATSAPP:
		return models.ChannelWhatsApp, true
	default:
		return "", false
	}
}

func channelToProto(ch models.Channel) notificationv1.Channel {
	switch ch {
	case models.ChannelInApp:
		return notificationv1.Channel_CHANNEL_IN_APP
	case models.ChannelPush:
		return notificationv1.Channel_CHANNEL_PUSH
	case models.ChannelEmail:
		return notificationv1.Channel_CHANNEL_EMAIL
	case models.ChannelWhatsApp:
		return notificationv1.Channel_CHANNEL_WHATSAPP
	default:
		return notificationv1.Channel_CHANNEL_UNSPECIFIED
	}
}

func toProtoResults(results []services.ChannelResult) []*notificationv1.ChannelResult {
	out := make([]*notificationv1.ChannelResult, 0, len(results))
	for _, r := range results {
		out = append(out, &notificationv1.ChannelResult{
			Channel:        channelToProto(r.Channel),
			Success:        r.Success,
			RecipientCount: safeconv.NonNegativeInt32(r.RecipientCount),
			Error:          r.Error,
		})
	}
	return out
}

func toProtoNotification(n *models.Notification) *notificationv1.Notification {
	out := &notificationv1.Notification{
		Id:          n.ID.Hex(),
		Event:       notificationv1.EventType(notificationv1.EventType_value[n.Event]),
		Title:       n.Title,
		Description: n.Body,
		SubjectId:   n.SubjectID,
		SubjectType: n.SubjectType,
		Read:        n.Read,
		CreatedAt:   timestamppb.New(n.CreatedAt),
	}
	if len(n.Params) > 0 {
		if s, err := structpb.NewStruct(n.Params); err == nil {
			out.Params = s
		}
	}
	return out
}

func repositoryFields() query.FieldSet {
	return query.FieldSet{
		"event":       "event",
		"read":        "read",
		"subjectType": "subjectType",
		"createdAt":   "createdAt",
	}
}

func mapError(err error) error {
	switch {
	case errors.Is(err, services.ErrValidation):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, services.ErrOTPRateLimited):
		return status.Error(codes.ResourceExhausted, err.Error())
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
