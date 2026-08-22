// Package clients holds this service's outbound gRPC connections.
package clients

import (
	"context"
	"fmt"

	"github.com/karlo/notification-service/internal/platform/authctx"
	authv1 "github.com/karlo/notification-service/internal/platform/genproto/karlo/auth/v1"
	commonv1 "github.com/karlo/notification-service/internal/platform/genproto/karlo/common/v1"
	"github.com/karlo/notification-service/internal/platform/grpcutil"
	"google.golang.org/grpc"
)

// Auth resolves recipients.
//
// This is the dependency that lets business services say "notify the
// transporter's dispatchers" without holding a copy of anyone's contact
// details. The authentication service owns push tokens, emails and phone
// numbers; this service borrows them at send time and never stores them.
type Auth struct {
	conn   *grpc.ClientConn
	client authv1.AuthServiceClient
}

func NewAuth(target, serviceName, serviceToken string) (*Auth, error) {
	conn, err := grpcutil.Dial(grpcutil.DialConfig{
		Service: serviceName, Target: target, ServiceToken: serviceToken,
	})
	if err != nil {
		return nil, err
	}
	return &Auth{conn: conn, client: authv1.NewAuthServiceClient(conn)}, nil
}

func (a *Auth) Close() error { return a.conn.Close() }

// ValidateToken satisfies authctx.RemoteValidator.
func (a *Auth) ValidateToken(ctx context.Context, token string) (authctx.Principal, error) {
	resp, err := a.client.ValidateToken(ctx, &authv1.ValidateTokenRequest{Token: token})
	if err != nil {
		return authctx.Principal{}, fmt.Errorf("clients: validate token: %w", err)
	}
	if !resp.GetValid() {
		return authctx.Principal{}, fmt.Errorf("clients: %s", resp.GetReason())
	}

	return principalFrom(resp.GetUser()), nil
}

// principalFrom maps the authentication service's user into a principal.
//
// The per-product access map is copied wholesale: this service resolves its own
// product through authctx, and flattening here would discard the other
// product's access from a token that legitimately carries both.
func principalFrom(user *authv1.User) authctx.Principal {
	p := authctx.Principal{
		UserID:          user.GetId(),
		CompanyID:       user.GetCompanyId(),
		ParentID:        user.GetParentId(),
		IsPlatformStaff: user.GetIsPlatformStaff(),
		FMSTenantID:     user.GetFmsTenantId(),
	}

	if access := user.GetAccess(); len(access) > 0 {
		p.Access = make(map[authctx.Product]authctx.ProductAccess, len(access))
		for product, a := range access {
			p.Access[authctx.Product(product)] = authctx.ProductAccess{
				Role:        a.GetRole(),
				Permissions: a.GetPermissions(),
				Features:    a.GetFeatures(),
			}
		}
	}

	return p
}

// Target is one resolved recipient's addresses.
type Target struct {
	UserID     string
	PushTokens []string
	Email      string
	Phone      string
	Language   string
	ExtraEmail []string
}

// ResolveUsers returns delivery targets for specific users.
//
// The auth service returns one row per push token, so a user with two devices
// appears twice. They are folded back into one target per user here, because
// the notification is per person even when delivery is per device.
func (a *Auth) ResolveUsers(ctx context.Context, userIDs []string) ([]Target, error) {
	if len(userIDs) == 0 {
		return nil, nil
	}

	resp, err := a.client.ResolveDeliveryTargets(ctx, &authv1.ResolveDeliveryTargetsRequest{
		UserIds: userIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("clients: resolve delivery targets: %w", err)
	}

	byUser := make(map[string]*Target, len(userIDs))
	order := make([]string, 0, len(userIDs))

	for _, t := range resp.GetTargets() {
		existing, ok := byUser[t.GetUserId()]
		if !ok {
			existing = &Target{
				UserID:     t.GetUserId(),
				Email:      t.GetEmail(),
				Phone:      t.GetPhone(),
				Language:   t.GetLanguage(),
				ExtraEmail: t.GetExtraEmailRecipients(),
			}
			byUser[t.GetUserId()] = existing
			order = append(order, t.GetUserId())
		}
		if token := t.GetPushToken(); token != "" {
			existing.PushTokens = append(existing.PushTokens, token)
		}
	}

	out := make([]Target, 0, len(order))
	for _, id := range order {
		out = append(out, *byUser[id])
	}
	return out, nil
}

// ResolveCompanyRoles expands "everyone in this company holding these roles"
// into a list of users, then resolves their addresses.
func (a *Auth) ResolveCompanyRoles(ctx context.Context, companyID string, roles []string) ([]Target, error) {
	if companyID == "" {
		return nil, fmt.Errorf("clients: company id is required")
	}

	// A company audience is bounded: the page size caps how many people one
	// event can reach, so a misconfigured audience cannot fan out without limit.
	resp, err := a.client.ListCompanyMembers(ctx, &authv1.ListCompanyMembersRequest{
		CompanyId: companyID,
		Roles:     roles,
		Query: &commonv1.Query{
			Page: &commonv1.Page{Page: 0, PageSize: 200},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("clients: list company members: %w", err)
	}

	ids := make([]string, 0, len(resp.GetUsers()))
	for _, u := range resp.GetUsers() {
		if u.GetIsSuspended() || u.GetDeleted() {
			continue
		}
		ids = append(ids, u.GetId())
	}

	return a.ResolveUsers(ctx, ids)
}
