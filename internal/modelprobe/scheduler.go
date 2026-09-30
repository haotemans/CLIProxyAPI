package modelprobe

import (
	"context"
	"strings"
	"sync"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// AuthsFunc lists probe candidates (all credentials registered in the process).
type AuthsFunc func() []*cliproxyauth.Auth

// CatalogFunc resolves the advertised model IDs for one credential — usually
// the registry registration for that auth (post alias/config narrowing).
type CatalogFunc func(auth *cliproxyauth.Auth) []string

// Scheduler runs periodic probe cycles and one-off per-credential probes.
type Scheduler struct {
	engine  *Engine
	store   *Store
	auths   AuthsFunc
	catalog CatalogFunc

	interval  time.Duration
	newTicker func(time.Duration) (<-chan time.Time, func())

	mu        sync.Mutex
	workQueue chan string
	done      chan struct{}
	wg        sync.WaitGroup
	started   bool
	running   bool

	// OnCycleComplete receives the number of credentials probed (observability).
	OnCycleComplete func(probed int)
}

// SchedulerOptions tunes the scheduler.
type SchedulerOptions struct {
	Interval time.Duration
	// NewTicker fakes the clock in tests; defaults to time.NewTicker.
	NewTicker func(time.Duration) (<-chan time.Time, func())
}

// NewScheduler wires an engine/store/credential+catalog supplier.
func NewScheduler(engine *Engine, store *Store, auths AuthsFunc, catalog CatalogFunc, opts SchedulerOptions) *Scheduler {
	newTicker := opts.NewTicker
	if newTicker == nil {
		newTicker = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTicker(d)
			return t.C, t.Stop
		}
	}
	return &Scheduler{
		engine:    engine,
		store:     store,
		auths:     auths,
		catalog:   catalog,
		interval:  opts.Interval,
		newTicker: newTicker,
		workQueue: make(chan string, 64),
		done:      make(chan struct{}),
	}
}

// Start launches the periodic processor. First boot cycle runs asynchronously
// immediately (credentials registered before scheduler start, probe right away).
func (s *Scheduler) Start(ctx context.Context) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	s.wg.Add(1)
	go s.loop(ctx)
}

// Stop shuts the scheduler down after the current cycle.
func (s *Scheduler) Stop() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return
	}
	s.started = false
	close(s.done)
	s.mu.Unlock()
	s.wg.Wait()
}

func (s *Scheduler) loop(ctx context.Context) {
	defer s.wg.Done()
	if ctx == nil {
		ctx = context.Background()
	}
	if s.interval <= 0 {
		s.interval = 6 * time.Hour
	}
	ticks, stop := s.newTicker(s.interval)
	defer stop()
	// Boot cycle straight away so fresh deployments populate sections quickly.
	s.safeCycle(ctx)
	for {
		select {
		case <-s.done:
			return
		case <-ctx.Done():
			return
		case <-ticks:
			s.safeCycle(ctx)
		}
	}
}

// safeCycle protects against panics in a cycle (they must never take the
// request path down — scheduler restarts keep running quietly).
func (s *Scheduler) safeCycle(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("modelprobe: probe cycle panic recovered: %v", r)
		}
	}()
	probed := s.RunCycle(ctx)
	if hook := s.OnCycleComplete; hook != nil {
		hook(probed)
	}
}

// RunCycle executes one full probe pass synchronously and returns how many
// credentials were probed this cycle.
func (s *Scheduler) RunCycle(ctx context.Context) int {
	if s == nil || s.engine == nil || s.auths == nil {
		return 0
	}
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return 0
	}
	s.running = true
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}()

	probed := 0
	for _, auth := range s.auths() {
		if ctx.Err() != nil {
			break
		}
		if !s.probeAuth(ctx, auth) {
			continue
		}
		probed++
	}
	return probed
}

// probeAuth runs one credential through the probe-persist pipeline. Returns
// true when a probe actually ran (driver exists, candidates available).
// Panics coming out of probe execution are contained per credential: one
// bad driver must never abort the cycle or the request path.
func (s *Scheduler) probeAuth(ctx context.Context, auth *cliproxyauth.Auth) (ran bool) {
	if auth == nil || auth.Disabled {
		return false
	}
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("modelprobe: credential probe panic for %s: %v", auth.ID, r)
			ran = false
		}
	}()
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if !s.engine.HasDriver(provider) {
		return false
	}
	models := s.candidates(auth)
	if len(models) == 0 {
		return false
	}
	section := s.engine.CredentialCycle(ctx, auth, provider, models)
	if section == nil {
		return false
	}
	if s.store != nil {
		s.store.ApplyOutcome(auth, section)
	}
	return true
}

// candidates resolves the model set to probe: registered catalog ids minus
// critically tiny dedupe; falls back to the previous section rows when the
// registry list is empty (auth just added and not yet re-registered).
func (s *Scheduler) candidates(auth *cliproxyauth.Auth) []string {
	if s.catalog != nil {
		if ids := s.catalog(auth); len(ids) > 0 {
			return ids
		}
	}
	if section := ReadSection(auth.Metadata); section != nil {
		ids := make([]string, 0, len(section.PerModel))
		for id := range section.PerModel {
			ids = append(ids, id)
		}
		return ids
	}
	return nil
}

// TriggerAuth runs one credential's probe inline (manual or first-import).
// Returns the merged Section (nil when the credential is not probe-backed).
func (s *Scheduler) TriggerAuth(ctx context.Context, auth *cliproxyauth.Auth) *Section {
	if s == nil || auth == nil || auth.Disabled {
		return nil
	}
	if !s.probeAuth(ctx, auth) {
		return nil
	}
	return ReadSection(auth.Metadata)
}

// TriggerAuthAsync posts one credential's probe to the background queue.
func (s *Scheduler) TriggerAuthAsync(ctx context.Context, auth *cliproxyauth.Auth) {
	if s == nil || auth == nil {
		return
	}
	go s.TriggerAuth(ctx, auth)
}
