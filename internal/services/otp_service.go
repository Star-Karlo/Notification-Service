package services

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/karlo/notification-service/internal/channels"
	"github.com/karlo/notification-service/internal/config"
	"github.com/karlo/notification-service/internal/models"
	notificationv1 "github.com/karlo/notification-service/internal/platform/genproto/karlo/notification/v1"
	"github.com/karlo/notification-service/internal/platform/safeconv"
	"github.com/karlo/notification-service/internal/repository"
	"github.com/karlo/notification-service/internal/templates"
)

// OTP errors.
var (
	ErrOTPRateLimited = errors.New("please wait before requesting another code")
	ErrOTPInvalid     = errors.New("invalid or expired code")
	ErrOTPExhausted   = errors.New("too many attempts")
)

// OTPService issues and verifies one-time passwords.
//
// Four things differ from the legacy implementation, each of which was a real
// weakness:
//
//   - Codes come from crypto/rand, not math/rand seeded from the clock.
//   - Only a hash is stored, so database access does not yield live codes.
//   - Attempts are capped, so a six-digit code cannot be brute-forced.
//   - Resends are throttled, so the endpoint cannot be used to send unlimited
//     WhatsApp messages to an arbitrary number.
type OTPService struct {
	repo     *repository.OTPRepository
	whatsapp channels.Sender
	cfg      config.OTP
}

func NewOTPService(repo *repository.OTPRepository, whatsapp channels.Sender, cfg config.OTP) *OTPService {
	return &OTPService{repo: repo, whatsapp: whatsapp, cfg: cfg}
}

// SendResult reports the outcome of an issue request.
type SendResult struct {
	Sent              bool
	ExpiresAt         time.Time
	RetryAfterSeconds int
}

// Send issues a code and delivers it over WhatsApp.
func (s *OTPService) Send(ctx context.Context, rawPhone, purpose, language string) (*SendResult, error) {
	phone, err := channels.NormalisePhone(rawPhone)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	if purpose == "" {
		purpose = "verification"
	}

	// Throttle resends before doing any work.
	lastIssued, err := s.repo.LastIssuedAt(ctx, phone, purpose)
	if err != nil {
		return nil, err
	}
	if !lastIssued.IsZero() {
		if wait := s.cfg.ResendCooldown - time.Since(lastIssued); wait > 0 {
			return &SendResult{
				Sent:              false,
				RetryAfterSeconds: int(wait.Seconds()) + 1,
			}, ErrOTPRateLimited
		}
	}

	code, err := generateCode(s.cfg.Length)
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	otp := &models.OTP{
		PhoneNumber: phone,
		CodeHash:    hashCode(code),
		Purpose:     purpose,
		ExpiresAt:   now.Add(s.cfg.TTL),
		CreatedAt:   now,
	}
	if err := s.repo.Create(ctx, otp); err != nil {
		return nil, err
	}

	copy, err := templates.Render(
		notificationv1.EventType_EVENT_TYPE_OTP,
		language,
		map[string]interface{}{
			"code":    code,
			"minutes": fmt.Sprintf("%d", int(s.cfg.TTL.Minutes())),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("otp: render message: %w", err)
	}

	if !s.whatsapp.Enabled() {
		return nil, fmt.Errorf("otp: WhatsApp is not configured, cannot deliver codes")
	}

	err = s.whatsapp.Send(ctx, channels.Message{
		Target:   phone,
		Title:    copy.Title,
		Body:     copy.Body,
		Data:     copy.Data,
		Template: copy.WhatsAppTemplate,
		Language: language,
	})
	if err != nil {
		// The code exists but could not be delivered. Report the failure rather
		// than claiming success, so the caller can offer a retry.
		return nil, fmt.Errorf("otp: deliver code: %w", err)
	}

	return &SendResult{Sent: true, ExpiresAt: otp.ExpiresAt}, nil
}

// VerifyResult reports a verification outcome.
type VerifyResult struct {
	Verified          bool
	Reason            string
	AttemptsRemaining int
}

// Verify checks a code and consumes it.
func (s *OTPService) Verify(ctx context.Context, rawPhone, code, purpose string) (*VerifyResult, error) {
	phone, err := channels.NormalisePhone(rawPhone)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	if purpose == "" {
		purpose = "verification"
	}

	otp, err := s.repo.FindLatest(ctx, phone, purpose)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return &VerifyResult{Verified: false, Reason: ErrOTPInvalid.Error()}, nil
		}
		return nil, err
	}

	if otp.IsSpent(s.cfg.MaxAttempt) {
		reason := ErrOTPInvalid.Error()
		if otp.Attempts >= s.cfg.MaxAttempt {
			reason = ErrOTPExhausted.Error()
		}
		return &VerifyResult{Verified: false, Reason: reason}, nil
	}

	// Constant-time comparison: a timing-variable one leaks the code digit by
	// digit to an attacker who can measure response times.
	if subtle.ConstantTimeCompare([]byte(hashCode(strings.TrimSpace(code))), []byte(otp.CodeHash)) != 1 {
		attempts, ierr := s.repo.IncrementAttempts(ctx, otp.ID)
		if ierr != nil {
			return nil, ierr
		}
		remaining := s.cfg.MaxAttempt - attempts
		if remaining < 0 {
			remaining = 0
		}
		return &VerifyResult{
			Verified:          false,
			Reason:            ErrOTPInvalid.Error(),
			AttemptsRemaining: remaining,
		}, nil
	}

	if err := s.repo.MarkVerified(ctx, otp.ID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			// Another request consumed it first.
			return &VerifyResult{Verified: false, Reason: ErrOTPInvalid.Error()}, nil
		}
		return nil, err
	}

	return &VerifyResult{Verified: true}, nil
}

// generateCode produces a numeric code using a cryptographic source.
//
// crypto/rand, not math/rand: the legacy implementation used an unseeded
// math/rand, whose output is reproducible and therefore predictable to anyone
// who can observe a few codes.
func generateCode(length int) (string, error) {
	digits := make([]byte, length)
	for i := range digits {
		n, err := rand.Int(rand.Reader, big.NewInt(10))
		if err != nil {
			return "", fmt.Errorf("otp: generate code: %w", err)
		}
		digits[i] = safeconv.Digit(n.Int64())
	}
	return string(digits), nil
}

// hashCode hashes a code for storage. SHA-256 is appropriate here: the input is
// short-lived and rate-limited, and lookups must be fast.
func hashCode(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}
