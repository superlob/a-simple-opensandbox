package sandbox

import (
	"context"
	"sync"
	"testing"
	"time"

	"sandbox/runtime/internal/config"
	"sandbox/runtime/internal/endpoint"
)

// clock is a settable time source for TTL tests.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newClockedService(t *testing.T, f *fakeDocker, c *clock, mutate func(*config.Config)) (*Service, *endpoint.Registry) {
	t.Helper()
	cfg := testConfig(t)
	if mutate != nil {
		mutate(&cfg)
	}
	reg := endpoint.NewRegistry()
	return NewService(cfg, f, reg, Options{PollInterval: time.Millisecond, Now: c.now}), reg
}

func TestRecoverRestoresEndpointsAfterRestart(t *testing.T) {
	f := newFakeDocker()
	c := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	svc1, _ := newClockedService(t, f, c, nil)
	sb, err := svc1.Create(context.Background(), CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}

	// Simulate a Runtime restart: a new service and an empty registry over the same daemon.
	svc2, reg2 := newClockedService(t, f, c, nil)
	if _, ok := reg2.Get(sb.ID); ok {
		t.Fatal("fresh registry should be empty")
	}
	rep, err := svc2.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if got, ok := reg2.Get(sb.ID); !ok || got != sb.Endpoint {
		t.Errorf("endpoint after recovery = %q %v, want %q", got, ok, sb.Endpoint)
	}
	if len(rep.Adopted) != 1 || rep.Adopted[0] != sb.ID {
		t.Errorf("adopted = %v", rep.Adopted)
	}
}

func TestRecoverRemovesCreatedLeftovers(t *testing.T) {
	f := newFakeDocker()
	c := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	svc, _ := newClockedService(t, f, c, func(cfg *config.Config) { cfg.RecoverLeftovers = true })

	// A create that died before start leaves a "created" container behind.
	id := "11111111-1111-4111-8111-111111111111"
	f.containers[NamePrefix+id] = &fakeContainer{
		spec: specWithLabels(map[string]string{
			LabelManaged: "true", LabelID: id, LabelCreatedAt: c.now().Format(time.RFC3339),
		}),
		status: "created",
		copied: map[string][]byte{},
	}

	rep, err := svc.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 1 || rep.Removed[0] != id {
		t.Errorf("removed = %v", rep.Removed)
	}
	if _, ok := f.containers[NamePrefix+id]; ok {
		t.Error("leftover container still present")
	}
}

func TestRecoverKeepsRunningUnhealthyAndReportsIt(t *testing.T) {
	f := newFakeDocker()
	c := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	svc, _ := newClockedService(t, f, c, nil)

	id := "22222222-2222-4222-8222-222222222222"
	f.containers[NamePrefix+id] = &fakeContainer{
		spec: specWithLabels(map[string]string{
			LabelManaged: "true", LabelID: id, LabelCreatedAt: c.now().Format(time.RFC3339),
		}),
		status:      "running",
		healthPolls: -1,
		copied:      map[string][]byte{},
	}

	rep, err := svc.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Unready) != 1 || rep.Unready[0] != id {
		t.Errorf("unready = %v", rep.Unready)
	}
	if _, ok := f.containers[NamePrefix+id]; !ok {
		t.Error("running container must not be removed by recovery")
	}
}

func TestRecoverLeavesStoppedAndForeignAlone(t *testing.T) {
	f := newFakeDocker()
	c := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	svc, reg := newClockedService(t, f, c, nil)
	sb, err := svc.Create(context.Background(), CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Stop(context.Background(), sb.ID); err != nil {
		t.Fatal(err)
	}
	f.containers["sbx-foreign"] = &fakeContainer{spec: specWithLabels(nil), status: "created", copied: map[string][]byte{}}

	rep, err := svc.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 0 || len(rep.Adopted) != 0 {
		t.Errorf("report = %+v", rep)
	}
	if _, ok := reg.Get(sb.ID); ok {
		t.Error("stopped sandbox must not have an endpoint")
	}
	if _, ok := f.containers["sbx-foreign"]; !ok {
		t.Error("foreign container was touched")
	}
	if _, ok := f.containers[NamePrefix+sb.ID]; !ok {
		t.Error("stopped sandbox must be kept")
	}
}

func TestRecoverIsIdempotent(t *testing.T) {
	f := newFakeDocker()
	c := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	svc, _ := newClockedService(t, f, c, nil)
	sb, err := svc.Create(context.Background(), CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		rep, err := svc.Recover(context.Background())
		if err != nil || len(rep.Adopted) != 1 || rep.Adopted[0] != sb.ID {
			t.Fatalf("pass %d: rep=%+v err=%v", i, rep, err)
		}
	}
}

func TestReapExpiredRemovesOnlyOldSandboxes(t *testing.T) {
	f := newFakeDocker()
	c := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	svc, reg := newClockedService(t, f, c, func(cfg *config.Config) {
		cfg.SandboxTTL = config.Duration(time.Hour)
	})
	old, err := svc.Create(context.Background(), CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}
	c.advance(90 * time.Minute)
	young, err := svc.Create(context.Background(), CreateRequest{})
	if err != nil {
		t.Fatal(err)
	}

	removed, errs := svc.ReapExpired(context.Background())
	if len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if len(removed) != 1 || removed[0] != old.ID {
		t.Errorf("removed = %v, want [%s]", removed, old.ID)
	}
	if _, ok := f.containers[NamePrefix+young.ID]; !ok {
		t.Error("young sandbox must survive")
	}
	if _, ok := reg.Get(old.ID); ok {
		t.Error("reaped sandbox must lose its endpoint")
	}
}

func TestReapExpiredDisabledWhenTTLZero(t *testing.T) {
	f := newFakeDocker()
	c := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	svc, _ := newClockedService(t, f, c, nil)
	if _, err := svc.Create(context.Background(), CreateRequest{}); err != nil {
		t.Fatal(err)
	}
	c.advance(1000 * time.Hour)
	removed, errs := svc.ReapExpired(context.Background())
	if len(removed) != 0 || len(errs) != 0 {
		t.Errorf("reaping should be disabled: removed=%v errs=%v", removed, errs)
	}
}

func TestReapExpiredCollectsErrorsAndContinues(t *testing.T) {
	f := newFakeDocker()
	c := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	svc, _ := newClockedService(t, f, c, func(cfg *config.Config) {
		cfg.SandboxTTL = config.Duration(time.Minute)
	})
	for i := 0; i < 2; i++ {
		if _, err := svc.Create(context.Background(), CreateRequest{}); err != nil {
			t.Fatal(err)
		}
	}
	c.advance(2 * time.Minute)
	f.failRemove = errBoom
	removed, errs := svc.ReapExpired(context.Background())
	if len(errs) != 2 || len(removed) != 0 {
		t.Errorf("removed=%v errs=%v; both failures should be collected", removed, errs)
	}
	f.failRemove = nil
	removed, errs = svc.ReapExpired(context.Background())
	if len(errs) != 0 || len(removed) != 2 {
		t.Errorf("retry: removed=%v errs=%v", removed, errs)
	}
}

func TestRunReaperStopsOnCancel(t *testing.T) {
	f := newFakeDocker()
	c := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	svc, _ := newClockedService(t, f, c, func(cfg *config.Config) {
		cfg.SandboxTTL = config.Duration(time.Minute)
	})
	if _, err := svc.Create(context.Background(), CreateRequest{}); err != nil {
		t.Fatal(err)
	}
	c.advance(2 * time.Minute)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	reported := make(chan []string, 1)
	go func() {
		svc.RunReaper(ctx, time.Millisecond, func(removed []string, _ []error) {
			select {
			case reported <- removed:
			default:
			}
		})
		close(done)
	}()
	select {
	case removed := <-reported:
		if len(removed) != 1 {
			t.Errorf("reported removed = %v", removed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reaper did not run")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("reaper did not stop after cancel")
	}
}

func TestRecoverReportsLeftoversByDefault(t *testing.T) {
	f := newFakeDocker()
	c := &clock{t: time.Date(2026, 11, 11, 10, 0, 0, 0, time.UTC)}
	svc, _ := newClockedService(t, f, c, nil) // RecoverLeftovers defaults to false

	id := "33333333-3333-4333-8333-333333333333"
	f.containers[NamePrefix+id] = &fakeContainer{
		spec: specWithLabels(map[string]string{
			LabelManaged: "true", LabelID: id, LabelCreatedAt: c.now().Format(time.RFC3339),
		}),
		status: "created",
		copied: map[string][]byte{},
	}
	rep, err := svc.Recover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Removed) != 0 || len(rep.Leftovers) != 1 || rep.Leftovers[0] != id {
		t.Errorf("report = %+v; leftover should be reported only", rep)
	}
	if _, ok := f.containers[NamePrefix+id]; !ok {
		t.Error("leftover removed without opt-in")
	}
}
