// Derived from KIDA-MNESIA/cpa-plugin-mirasim (MIT): post-login credential
// installation (device identity, profile read, relay validation gate).
package mirasim

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// finalizeTimeout bounds the remote validation after login (credential
// acquisition; the one spot bounded timeouts are allowed).
const finalizeTimeout = 60 * time.Second

// FinalizeOAuthStorage builds and remotely validates the credential after a
// successful browser or email login: tokens + fresh device identity are
// installed, the profile is read best-effort, and a signed GET /v1/models
// proves the token and device identity work before the file is persisted.
func FinalizeOAuthStorage(ctx context.Context, settings Settings, accessToken, refreshToken string) (Storage, error) {
	settings = settings.Normalize()
	storage, err := InstallOAuth(NewStorage(settings), accessToken, refreshToken)
	if err != nil {
		return Storage{}, err
	}
	// Identity claims affect only naming and labels after the token passes
	// the authenticated relay validation below.
	storage.PopulateIdentityFromAccessToken()

	// Mirasim's official client treats /auth/me as best-effort during login;
	// capture its plan state when reachable, keeping the signed relay
	// validation as the persistence gate.
	if profile, errProfile := FetchAccountProfile(ctx, storage.AdminURL, storage.AccessToken, settings.ProxyURL); errProfile == nil {
		storage.RecordProfile(profile.Email, profile.Plan, profile.PlanExpiresAt, time.Now())
	}

	if err := ValidateRemoteWithStorage(ctx, &storage, settings.ProxyURL); err != nil {
		return Storage{}, err
	}
	return storage, nil
}

// ValidateRemoteWithStorage proves the OAuth token and generated device
// identity can mint a ticket and read the authenticated model catalog before
// the login is persisted.
func ValidateRemoteWithStorage(ctx context.Context, storage *Storage, proxyURL string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	client, errClient := NewRelayClient(storage, proxyURL)
	if errClient != nil {
		return errClient
	}
	validationCtx, cancel := context.WithTimeout(ctx, finalizeTimeout)
	defer cancel()
	if _, _, err := client.FetchModels(validationCtx); err != nil {
		var status interface{ StatusCode() int }
		if errors.As(err, &status) && status != nil && status.StatusCode() > 0 {
			return fmt.Errorf("validate Mirasim OAuth credentials: upstream returned HTTP %d", status.StatusCode())
		}
		return fmt.Errorf("validate Mirasim OAuth credentials: %w", err)
	}
	return nil
}

// NextRefreshAfter computes when the credential should return to the
// refresh lifecycle: the earlier of the token's refresh lead and the next
// periodic profile check.
func NextRefreshAfter(storage *Storage, now time.Time) time.Time {
	if storage == nil {
		return now
	}
	if now.IsZero() {
		now = time.Now()
	}
	next := storage.AccessTokenExpiry(now).Add(-AccessRefreshLead)
	profileNext := now
	if checkedAt := storage.ProfileCheckTime(); !checkedAt.IsZero() {
		profileNext = checkedAt.Add(ProfileRefreshInterval)
	}
	if profileNext.Before(next) {
		next = profileNext
	}
	if next.Before(now) {
		return now
	}
	return next
}

// AccessRefreshLead is how far ahead of expiry a refresh is scheduled; the
// official client allows itself the same quarter hour.
const AccessRefreshLead = 15 * time.Minute

// RefreshForSchedule performs the periodic refresh entry point: it refreshes
// the profile when due and refreshes the access token when it (or a detected
// plan change) calls for one, mirroring the plugin's RefreshForHost.
func RefreshForSchedule(ctx context.Context, storage *Storage, proxyURL string, now time.Time) error {
	if storage == nil {
		return errMissingToken
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if now.IsZero() {
		now = time.Now()
	}
	checkedAt := storage.ProfileCheckTime()
	profileDue := checkedAt.IsZero() || !now.Before(checkedAt.Add(ProfileRefreshInterval))
	accessDue := !now.Before(storage.AccessTokenExpiry(now).Add(-AccessRefreshLead))
	mustRefresh := accessDue

	var observed *AccountProfile
	if profileDue {
		if profile, errProfile := FetchAccountProfile(ctx, storage.AdminURL, storage.AccessToken, proxyURL); errProfile == nil {
			observed = &profile
			profileChanged := checkedAt.IsZero() || !sameInt64Ptr(profile.PlanExpiresAt, storage.PlanExpiresAt) || profile.Plan != storage.Plan
			tokenPlan, tokenPlanExpiresAt := AccessTokenPlan(storage.AccessToken)
			planMismatch := profile.Plan != "" && (profile.Plan != tokenPlan || profile.PlanExpiryKnown && !sameInt64Ptr(profile.PlanExpiresAt, tokenPlanExpiresAt))
			mustRefresh = mustRefresh || profileChanged && planMismatch
		}
	} else if !mustRefresh {
		// A scheduled refresh before either boundary is a reactive/manual
		// refresh; preserve the 401 recovery semantics.
		mustRefresh = true
	}

	if mustRefresh {
		if err := RefreshAccessToken(ctx, storage, proxyURL); err != nil {
			return err
		}
	}
	if observed != nil {
		storage.RecordProfile(observed.Email, observed.Plan, observed.PlanExpiresAt, now)
	}
	return nil
}

func sameInt64Ptr(a, b *int64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
