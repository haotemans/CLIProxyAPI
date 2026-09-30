package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestEnsureModelProbeSchedulerDisabledByDefault(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	s.ensureModelProbeScheduler(nil)
	if s.modelProbe != nil {
		t.Fatal("scheduler must not start with empty config")
	}
}

func TestEnsureModelProbeSchedulerEnabledStartsAndStops(t *testing.T) {
	cfg := &config.Config{}
	cfg.ModelProbe.Enabled = true
	cfg.ModelProbe.Interval = 7_200
	s := &Service{cfg: cfg}
	s.ensureModelProbeScheduler(nil)
	if s.modelProbe == nil {
		t.Fatal("scheduler expected when enabled")
	}
	s.ensureModelProbeScheduler(nil) // idempotent double start
	s.ShutdownModelProbe()
	if s.modelProbe != nil {
		t.Fatal("ShutdownModelProbe must clear the scheduler")
	}
}

func TestMaybeProbeNewAuthNoopWhenDisabled(t *testing.T) {
	s := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{ID: "cursor-a.json", Provider: "cursor", Metadata: map[string]any{"access_token": "at"}}
	// Must not panic without coreManager or an enabled scheduler.
	s.maybeProbeNewAuth(nil, auth)
	if s.modelProbe != nil {
		t.Fatal("no scheduler when disabled")
	}
}
