package cliproxy

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/modelprobe"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// modelProbeDefaults mirrors config defaults without a hard dependency back
// to the YAML layer semantics (unit:
// 6h interval, 4 parallel, unlimited models per cycle).
const (
	modelProbeDefaultInterval    = 6 * time.Hour
	modelProbeDefaultMaxParallel = 4
)

// modelProbeSchedulerOnce guards the lazy scheduler bootstrap: the service
// starts the probe scheduler exactly once per process, the first time an
// auth update flows (so the initial credential registration exists already).
var _ = sync.Once{}

// ensureModelProbeScheduler starts the per-credential probe scheduler lazily
// when enabled. Called from handleAuthUpdates after registrations complete,
// so auths and registry registrations are already in a stable state.
func (s *Service) ensureModelProbeScheduler(ctx context.Context) {
	if s == nil || s.cfg == nil || !s.cfg.ModelProbe.Enabled {
		return
	}
	s.modelProbeMu.Lock()
	defer s.modelProbeMu.Unlock()
	if s.modelProbe != nil {
		return
	}
	opts := modelprobe.Options{
		MaxModelsPerCredentialPerCycle: s.cfg.ModelProbe.MaxModelsPerCredentialPerCycle,
		MaxParallel:                    modelProbeDefaultMaxParallel,
		ProbeTimeoutPerModel:           90 * time.Second,
	}
	if max := s.cfg.ModelProbe.MaxParallel; max > 0 {
		opts.MaxParallel = max
	}
	if spacingMin, spacingMax, errSpacing := modelprobe.SpacingRange(s.cfg.ModelProbe.ProbeSpacing); errSpacing != nil {
		log.Warnf("modelprobe: invalid probe-spacing %q, using default: %v", s.cfg.ModelProbe.ProbeSpacing, errSpacing)
		opts.SpacingMin, opts.SpacingMax, _ = modelprobe.SpacingRange("")
	} else {
		opts.SpacingMin, opts.SpacingMax = spacingMin, spacingMax
	}
	engine := modelprobe.NewEngine(s.cfg, opts)
	store := &modelprobe.Store{AuthDir: s.cfg.AuthDir}
	scheduler := modelprobe.NewScheduler(engine, store,
		s.modelProbeAuths,
		s.modelProbeCatalog,
		modelprobe.SchedulerOptions{
			Interval: s.modelProbeInterval(),
			Jitter:   s.cfg.ModelProbe.Jitter,
		},
	)
	s.modelProbe = scheduler
	scheduler.Start(ctx)
	log.Infof("modelprobe: scheduler started (interval %s, max-parallel %d)", s.modelProbeInterval(), opts.MaxParallel)
}

// modelProbeAuths lists registered auths for the probe engine.
func (s *Service) modelProbeAuths() []*coreauth.Auth {
	if s == nil || s.coreManager == nil {
		return nil
	}
	return s.coreManager.List()
}

// modelProbeCatalog returns the registry-registered model ids for a credential
// — the exact set currently advertised for that auth.
func (s *Service) modelProbeCatalog(auth *coreauth.Auth) []string {
	if s == nil || auth == nil {
		return nil
	}
	modelIDs, _ := registry.GetGlobalRegistry().GetModelsAndEpochForClient(auth.ID)
	ids := make([]string, 0, len(modelIDs))
	for _, model := range modelIDs {
		if model == nil || strings.TrimSpace(model.ID) == "" {
			continue
		}
		ids = append(ids, model.ID)
	}
	return ids
}

func (s *Service) modelProbeInterval() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.ModelProbe.Interval > 0 {
		return time.Duration(s.cfg.ModelProbe.Interval) * time.Second
	}
	return modelProbeDefaultInterval
}

// maybeProbeNewAuth runs a single fresh probe for one newly registered auth
// (first-import trigger). Disabled scheduler ⇒ no-op; results persist via the
// section the scheduler merges into the credential metadata.
func (s *Service) maybeProbeNewAuth(ctx context.Context, auth *coreauth.Auth) {
	if s == nil {
		return
	}
	s.ensureModelProbeScheduler(ctx)
	scheduler := s.modelProbeScheduler()
	if scheduler == nil || auth == nil || auth.Disabled {
		return
	}
	if section := modelprobe.ReadSection(auth.Metadata); section != nil {
		// Already probed once; interval cycles keep it fresh.
		return
	}
	scheduler.TriggerAuthAsync(ctx, auth)
}

func (s *Service) modelProbeScheduler() *modelprobe.Scheduler {
	if s == nil {
		return nil
	}
	s.modelProbeMu.Lock()
	defer s.modelProbeMu.Unlock()
	return s.modelProbe
}

// ShutdownModelProbe stops the probe scheduler (called on service shutdown).
func (s *Service) ShutdownModelProbe() {
	if s == nil {
		return
	}
	s.modelProbeMu.Lock()
	scheduler := s.modelProbe
	s.modelProbe = nil
	s.modelProbeMu.Unlock()
	if scheduler != nil {
		scheduler.Stop()
	}
}
