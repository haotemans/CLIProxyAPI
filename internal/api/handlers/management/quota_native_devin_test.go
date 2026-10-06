package management

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	devinauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/devin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"google.golang.org/protobuf/encoding/protowire"
)

// buildDevinQuotaMockResponse serializes the minimal GetUserStatus wire reply
// carrying a plan name plus daily/weekly quota windows.
func buildDevinQuotaMockResponse(plan string, dailyPercent, weeklyPercent, dailyReset, weeklyReset int64) []byte {
	var planInfo []byte
	planInfo = protowire.AppendTag(planInfo, 2, protowire.BytesType)
	planInfo = protowire.AppendString(planInfo, plan)

	var planStatus []byte
	planStatus = protowire.AppendTag(planStatus, 1, protowire.BytesType)
	planStatus = protowire.AppendBytes(planStatus, planInfo)

	planStatus = protowire.AppendTag(planStatus, 14, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, uint64(dailyPercent))
	planStatus = protowire.AppendTag(planStatus, 15, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, uint64(weeklyPercent))
	planStatus = protowire.AppendTag(planStatus, 17, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, uint64(dailyReset))
	planStatus = protowire.AppendTag(planStatus, 18, protowire.VarintType)
	planStatus = protowire.AppendVarint(planStatus, uint64(weeklyReset))

	var userStatus []byte
	userStatus = protowire.AppendTag(userStatus, 13, protowire.BytesType)
	userStatus = protowire.AppendBytes(userStatus, planStatus)

	var resp []byte
	resp = protowire.AppendTag(resp, 1, protowire.BytesType)
	resp = protowire.AppendBytes(resp, userStatus)
	return resp
}

// newDevinQuotaMockServer serves the seat management GetUserStatus endpoint;
// statusCode != 200 replays an upstream failure with a text body.
func newDevinQuotaMockServer(t *testing.T, statusCode int) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != devinauth.DevinGetUserStatusPath {
			http.NotFound(w, r)
			return
		}
		if statusCode != http.StatusOK {
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(statusCode)
			_, _ = w.Write([]byte("upstream failure"))
			return
		}
		w.Header().Set("Content-Type", "application/proto")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(buildDevinQuotaMockResponse("Pro", 100, 50, 1789200000, 1789286400))
	}))
}

func TestFetchDevinQuotaAPIKeyCredential(t *testing.T) {
	server := newDevinQuotaMockServer(t, http.StatusOK)
	defer server.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	auth := &cliproxyauth.Auth{
		Provider:   "devin",
		Attributes: map[string]string{"api_key": "tok", "base_url": server.URL},
	}

	resp, handled, err := h.tryNativeQuotaFetch(context.Background(), auth)
	if err != nil {
		t.Fatalf("tryNativeQuotaFetch error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for devin credential")
	}
	if resp.Subscription == nil || resp.Subscription.Plan != "Pro" {
		t.Fatalf("expected plan Pro, got %+v", resp.Subscription)
	}
	if len(resp.Groups) != 1 || len(resp.Groups[0].Buckets) != 2 {
		t.Fatalf("expected one group with two buckets, got %+v", resp.Groups)
	}
	daily := resp.Groups[0].Buckets[0]
	if daily.Window != "daily" || daily.RemainingFraction != 1.0 {
		t.Errorf("daily bucket = %+v, want window daily fraction 1.0", daily)
	}
	if daily.ResetTime != "2026-09-12T08:00:00Z" {
		t.Errorf("daily reset = %q, want 2026-09-12T08:00:00Z", daily.ResetTime)
	}
	weekly := resp.Groups[0].Buckets[1]
	if weekly.Window != "weekly" || weekly.RemainingFraction != 0.5 {
		t.Errorf("weekly bucket = %+v, want window weekly fraction 0.5", weekly)
	}
	if weekly.ResetTime != "2026-09-13T08:00:00Z" {
		t.Errorf("weekly reset = %q, want 2026-09-13T08:00:00Z", weekly.ResetTime)
	}
	if len(resp.Summary) != 2 {
		t.Fatalf("expected two summary metrics, got %+v", resp.Summary)
	}
	if resp.Summary[0].Key != "daily_quota_remaining" || resp.Summary[0].Value != 100 || resp.Summary[0].Unit != "%" {
		t.Errorf("daily summary metric = %+v", resp.Summary[0])
	}
	if resp.Summary[1].Key != "weekly_quota_remaining" || resp.Summary[1].Value != 50 || resp.Summary[1].Unit != "%" {
		t.Errorf("weekly summary metric = %+v", resp.Summary[1])
	}
}

func TestFetchDevinQuotaOAuthCredential(t *testing.T) {
	server := newDevinQuotaMockServer(t, http.StatusOK)
	defer server.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	auth := &cliproxyauth.Auth{
		Provider: "devin",
		Metadata: map[string]any{
			"access_token": "oauth-tok",
			"base_url":     server.URL,
			"auth_kind":    "oauth",
		},
	}

	resp, handled, err := h.tryNativeQuotaFetch(context.Background(), auth)
	if err != nil {
		t.Fatalf("tryNativeQuotaFetch error: %v", err)
	}
	if !handled {
		t.Fatal("expected handled=true for devin OAuth credential")
	}
	if resp.Subscription == nil || resp.Subscription.Plan != "Pro" {
		t.Fatalf("expected plan Pro, got %+v", resp.Subscription)
	}
	if len(resp.Groups) != 1 || len(resp.Groups[0].Buckets) != 2 {
		t.Fatalf("expected one group with two buckets, got %+v", resp.Groups)
	}
}

func TestFetchDevinQuotaMissingToken(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	auth := &cliproxyauth.Auth{Provider: "devin"}

	_, handled, err := h.tryNativeQuotaFetch(context.Background(), auth)
	if !handled {
		t.Fatal("expected handled=true for unusable devin credential")
	}
	if err == nil || !strings.Contains(err.Error(), "session token") {
		t.Fatalf("expected session token error, got %v", err)
	}
}

func TestFetchDevinQuotaUpstreamError(t *testing.T) {
	server := newDevinQuotaMockServer(t, http.StatusInternalServerError)
	defer server.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	auth := &cliproxyauth.Auth{
		Provider:   "devin",
		Attributes: map[string]string{"api_key": "tok", "base_url": server.URL},
	}

	_, handled, err := h.tryNativeQuotaFetch(context.Background(), auth)
	if !handled {
		t.Fatal("expected handled=true for devin credential")
	}
	if err == nil || !strings.Contains(err.Error(), "devin user status") {
		t.Fatalf("expected wrapped devin user status error, got %v", err)
	}
}

func TestNativeQuotaSupportedDevin(t *testing.T) {
	if !nativeQuotaSupported("devin") {
		t.Fatal("nativeQuotaSupported(devin) = false, want true")
	}
	if !nativeQuotaSupported("Devin") {
		t.Fatal("nativeQuotaSupported(Devin) = false, want true (case-insensitive)")
	}
}

func TestGetQuotaProvidersIncludesDevin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/quota/providers", nil)

	h.GetQuotaProviders(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, provider := range []string{"kiro", "mirasim", "devin"} {
		if !strings.Contains(body, fmt.Sprintf("%q", provider)) {
			t.Errorf("providers body %s missing %q", body, provider)
		}
	}
}
