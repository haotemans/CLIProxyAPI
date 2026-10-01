package modelprobe

import (
	"context"
	"math/rand"
	"strings"
	"sync"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

// Backoff cycle dividers: `limited` models are probed at most every 2nd
// cycle, auth_error-flagged credentials at most every 4th.
const (
	limitedBackoffEvery   = 2
	authErrorBackoffEvery = 4
)

// AuthsFunc lists probe candidates (all credentials registered in the process).
type AuthsFunc func() []*cliproxyauth.Auth

// CatalogFunc resolves the advertised model IDs for one credential — usually
// the registry registration for that auth (post alias/config narrowing).
type CatalogFunc func(auth *cliproxyauth.Auth) []string

// Scheduler runs periodic probe cycles and one-off per-credential probes.
// Every cycle's next start is jittered (interval*(1±jitter)) so the cadence
// does not read as a fixed bot pattern.
type Scheduler struct {
	engine  *Engine
	store   *Store
	auths   AuthsFunc
	catalog CatalogFunc

	interval   time.Duration
	jitter     float64
	jitterRand func() float64
	newTimer   func(time.Duration) (<-chan time.Time, func())

	mu          sync.Mutex
	done        chan struct{}
	wg          sync.WaitGroup
	started     bool
	running     bool
	cycleNumber uint64
	nextRunAt   time.Time

	// OnCycleComplete receives the number of credentials probed (observability).
	OnCycleComplete func(probed int)
}

// SchedulerOptions tunes the scheduler.
type SchedulerOptions struct {
	Interval time.Duration
	// Jitter is the fraction of interval randomization. Nil means the 0.5
	// default; a pointer to 0 restores an exact fixed cadence (detectable).
	Jitter *float64
	// JitterRand samples [0,1) for next-delay computation; tests inject
	// deterministic sources.
	JitterRand func() float64
	// NewTimer fakes the clock in tests; defaults to time.NewTimer.
	NewTimer func(time.Duration) (<-chan time.Time, func())
}

// NewScheduler wires an engine/store/credential+catalog supplier.
func NewScheduler(engine *Engine, store *Store, auths AuthsFunc, catalog CatalogFunc, opts SchedulerOptions) *Scheduler {
	jitter := DefaultJitter
	if opts.Jitter != nil {
		jitter = EffectiveJitter(*opts.Jitter)
	}
	newTimer := opts.NewTimer
	if newTimer == nil {
		newTimer = func(d time.Duration) (<-chan time.Time, func()) {
			t := time.NewTimer(d)
			return t.C, func() { t.Stop() }
		}
	}
	jitterRand := opts.JitterRand
	if jitterRand == nil {
		jitterRand = rand.Float64
	}
	return &Scheduler{
		engine:     engine,
		store:      store,
		auths:      auths,
		catalog:    catalog,
		interval:   opts.Interval,
		jitter:     jitter,
		jitterRand: jitterRand,
		newTimer:   newTimer,
		done:       make(chan struct{}),
	}
}

// Start launches the periodic processor. The boot cycle runs asynchronously
// immediately (credentials registered before scheduler start probe right away).
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

// NextRunAt returns the next scheduled cycle start (jitter-applied).
func (s *Scheduler) NextRunAt() (time.Time, bool) {
	if s == nil {
		return time.Time{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nextRunAt.IsZero() {
		return time.Time{}, false
	}
	return s.nextRunAt, true
}

func (s *Scheduler) loop(ctx context.Context) {
	defer s.wg.Done()
	if ctx == nil {
		ctx = context.Background()
	}
	if s.interval <= 0 {
		s.interval = 6 * time.Hour
	}
	// Boot cycle straight away so fresh deployments populate sections quickly.
	s.safeCycle(ctx)
	for {
		delay := jitteredInterval(s.interval, s.jitter, s.jitterRand())
		s.mu.Lock()
		s.nextRunAt = time.Now().Add(delay)
		s.mu.Unlock()
		SetNextRun(s.nextRunAt)
		timer, stop := s.newTimer(delay)
		select {
		case <-s.done:
			stop()
			return
		case <-ctx.Done():
			stop()
			return
		case <-timer:
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
	s.cycleNumber++
	cycle := s.cycleNumber
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
		if !s.probeAuth(ctx, auth, cycle) {
			continue
		}
		probed++
	}
	return probed
}

// probeAuth runs one credential through the probe-persist pipeline with the
// auth_error backoff rule (every 4th cycle only when previously flagged).
// Returns true when a probe actually ran (driver exists, candidates available).
// Panics coming out of probe execution are contained per credential: one bad
// driver must never abort the cycle or the request path.
func (s *Scheduler) probeAuth(ctx context.Context, auth *cliproxyauth.Auth, cycle uint64) (ran bool) {
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
	previous := ReadSection(auth.Metadata)
	if cycle%authErrorBackoffEvery != 0 && sectionHasAuthError(previous) {
		s.recordCredentialSkip(auth, previous, "auth_error backoff (every 4th cycle)", cycle)
		return false
	}
	models := s.candidates(auth)
	if len(models) == 0 {
		return false
	}
	models, skips := filterLimitedBackoff(models, previous, cycle)
	section := s.engine.CredentialCycle(ctx, auth, provider, models)
	if section == nil {
		return false
	}
	if len(skips) > 0 {
		section.Skips = skips
	}
	if s.store != nil {
		merged := s.store.ApplyOutcome(auth, section)
		if len(skips) > 0 && merged != nil {
			merged.Skips = mergeSkips(merged.Skips, section.Skips)
			if err := s.store.persistToFile(auth, merged); err != nil {
				log.Debugf("modelprobe: durable write skipped for %s: %v", auth.ID, err)
			}
		}
	}
	return true
}

// filterLimitedBackoff removes `limited` models on odd cycles (they re-enter
// on even ones), returning the survivors and skip reasons keyed by model ID.
func filterLimitedBackoff(models []string, previous *Section, cycle uint64) ([]string, map[string]string) {
	if previous == nil || len(previous.PerModel) == 0 || cycle%limitedBackoffEvery == 0 {
		return models, nil
	}
	out := make([]string, 0, len(models))
	var skips map[string]string
	for _, id := range models {
		key := strings.ToLower(strings.TrimSpace(id))
		if outcome, ok := previous.PerModel[key]; ok && outcome != nil && outcome.Status == StatusLimited {
			if skips == nil {
				skips = map[string]string{}
			}
			skips[key] = "limited backoff (every 2nd cycle)"
			continue
		}
		out = append(out, id)
	}
	return out, skips
}

// sectionHasAuthError flags credentials whose last probe had any auth_error.
func sectionHasAuthError(section *Section) bool {
	if section == nil {
		return false
	}
	for _, outcome := range section.PerModel {
		if outcome != nil && outcome.Status == StatusAuthError {
			return true
		}
	}
	return false
}

// recordCredentialSkip persists the skip decision without probing: previous
// section content is preserved, skip fields annotate the cycle.
func (s *Scheduler) recordCredentialSkip(auth *cliproxyauth.Auth, previous *Section, reason string, cycle uint64) {
	if previous == nil {
		return
	}
	annotate := &Section{
		CheckedAt:  previous.CheckedAt,
		Usable:     previous.Usable,
		Pruned:     previous.Pruned,
		PerModel:   previous.PerModel,
		Skipped:    true,
		SkipReason: reason,
		SkipCycle:  cycle,
		Skips:      previous.Skips,
	}
	if s.store != nil {
		s.store.ApplyOutcome(auth, annotate)
	}
}

func mergeSkips(previous, fresh map[string]string) map[string]string {
	if len(fresh) == 0 {
		return previous
	}
	out := make(map[string]string, len(previous)+len(fresh))
	for key, value := range previous {
		out[key] = value
	}
	for key, value := range fresh {
		out[key] = value
	}
	return out
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
// Manual triggers always probe (backoffs are a scheduled-cycle concern only).
func (s *Scheduler) TriggerAuth(ctx context.Context, auth *cliproxyauth.Auth) *Section {
	if s == nil || auth == nil || auth.Disabled {
		return nil
	}
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	if !s.engine.HasDriver(provider) {
		return nil
	}
	models := s.candidates(auth)
	if len(models) == 0 {
		return nil
	}
	section := s.engine.CredentialCycle(ctx, auth, provider, models)
	if section == nil {
		return nil
	}
	if s.store != nil {
		s.store.ApplyOutcome(auth, section)
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

// globalNextRun tracks the active scheduler's next cycle for the status
// endpoint; set by SetNextRun, read by NextRunAt.
var (
	globalNextRunMu sync.Mutex
	globalNextRunAt time.Time
)

// SetNextRun publishes the scheduler's next cycle start (jitter-applied).
func SetNextRun(at time.Time) {
	globalNextRunMu.Lock()
	globalNextRunAt = at
	globalNextRunMu.Unlock()
}

// ClearNextRun unsets the published next run (scheduler stopped).
func ClearNextRun() { SetNextRun(time.Time{}) }

// NextRunAt returns the published next cycle start (jitter-applied).
func NextRunAt() (time.Time, bool) {
	globalNextRunMu.Lock()
	defer globalNextRunMu.Unlock()
	if globalNextRunAt.IsZero() {
		return time.Time{}, false
	}
	return globalNextRunAt, true
}
