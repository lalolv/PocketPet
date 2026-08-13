package petstate

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
)

var t0 = time.Date(2026, 8, 12, 9, 0, 0, 0, time.UTC)

type memHost struct {
	mu    sync.Mutex
	pets  map[string]*pet.Pet
	locks map[string]*sync.Mutex
	evs   []pet.Event
	clock time.Time
}

func newMemHost(p *pet.Pet) *memHost {
	h := &memHost{
		pets:  map[string]*pet.Pet{p.ID: p},
		locks: map[string]*sync.Mutex{},
		clock: t0,
	}
	p.SyncSleepingFromActivity()
	return h
}

func (h *memHost) LockPet(id string) func() {
	h.mu.Lock()
	l, ok := h.locks[id]
	if !ok {
		l = &sync.Mutex{}
		h.locks[id] = l
	}
	h.mu.Unlock()
	l.Lock()
	return l.Unlock
}

func (h *memHost) LoadPet(_ context.Context, id string) (*pet.Pet, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	p, ok := h.pets[id]
	if !ok {
		return nil, errors.New("not found")
	}
	cp := *p
	cp.Intents = append([]string(nil), p.Intents...)
	return &cp, nil
}

func (h *memHost) SavePet(_ context.Context, p *pet.Pet) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	cp := *p
	cp.Intents = append([]string(nil), p.Intents...)
	h.pets[p.ID] = &cp
	return nil
}

func (h *memHost) TraitsOf(string) pet.Traits { return pet.NeutralTraits() }
func (h *memHost) Now() time.Time              { return h.clock }
func (h *memHost) OfflineMax() time.Duration   { return 24 * time.Hour }
func (h *memHost) Emit(_ context.Context, evs ...pet.Event) {
	h.mu.Lock()
	h.evs = append(h.evs, evs...)
	h.mu.Unlock()
}
func (h *memHost) PublishState(*pet.Pet) {}

func (h *memHost) get(id string) *pet.Pet {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.pets[id]
}

func TestApplySleepAndWake(t *testing.T) {
	p := pet.New("p1", "团团", "cat", t0)
	h := newMemHost(p)
	m := New(h)

	res, err := m.RequestSleep(context.Background(), "p1", "test")
	if err != nil || !res.OK || res.Snapshot.Activity.Kind != pet.ActivitySleeping {
		t.Fatalf("sleep = %+v, %v", res, err)
	}
	if !h.get("p1").Sleeping {
		t.Fatal("Sleeping flag")
	}

	res, err = m.GoIdle(context.Background(), "p1", "wake")
	if err != nil || !res.OK || res.Snapshot.Activity.Kind != pet.ActivityIdle {
		t.Fatalf("wake = %+v, %v", res, err)
	}
}

func TestSleepQueuesWhileAdventuring(t *testing.T) {
	p := pet.New("p1", "团团", "cat", t0)
	p.Stats.Energy = 80
	h := newMemHost(p)
	m := New(h)
	_ = m.RegisterKind(ActivityKind{Name: pet.ActivityAdventuring, Owner: "adventure"})

	res, err := m.Apply(context.Background(), "p1", Transition{
		To: pet.ActivityAdventuring, Owner: "adventure",
	})
	if err != nil || !res.OK {
		t.Fatalf("start = %+v, %v", res, err)
	}

	res, err = m.RequestSleep(context.Background(), "p1", "autosleep")
	if err != nil || !res.OK || !res.Queued {
		t.Fatalf("queue sleep = %+v, %v", res, err)
	}
	if h.get("p1").Activity != pet.ActivityAdventuring {
		t.Fatal("should stay adventuring")
	}
	if !h.get("p1").HasIntent(pet.IntentSleep) {
		t.Fatal("want intent sleep")
	}

	res, err = m.GoIdle(context.Background(), "p1", "finished")
	if err != nil || !res.OK {
		t.Fatalf("idle = %+v, %v", res, err)
	}
	if res.Snapshot.Activity.Kind != pet.ActivitySleeping {
		t.Fatalf("after idle want sleeping, got %+v", res.Snapshot.Activity)
	}
}

func TestRejectAdventureWhenSleepy(t *testing.T) {
	p := pet.New("p1", "团团", "cat", t0)
	p.Stats.Energy = 20
	h := newMemHost(p)
	m := New(h)
	_ = m.RegisterKind(ActivityKind{Name: pet.ActivityAdventuring, Owner: "adventure"})

	res, err := m.Apply(context.Background(), "p1", Transition{
		To: pet.ActivityAdventuring, Owner: "adventure",
	})
	if err != nil || res.OK || !errors.Is(res.Err, pet.ErrBusy) {
		t.Fatalf("want busy, got %+v %v", res, err)
	}
}

func TestOnCommitRollback(t *testing.T) {
	p := pet.New("p1", "团团", "cat", t0)
	p.Stats.Energy = 80
	h := newMemHost(p)
	m := New(h)
	_ = m.RegisterKind(ActivityKind{Name: pet.ActivityAdventuring, Owner: "adventure"})

	boom := errors.New("insert failed")
	res, err := m.Apply(context.Background(), "p1", Transition{
		To: pet.ActivityAdventuring, Owner: "adventure",
		StatsDelta: pet.Stats{Energy: -15},
		OnCommit:   func(context.Context, Snapshot, Snapshot) error { return boom },
	})
	if err != nil || res.OK || !errors.Is(res.Err, boom) {
		t.Fatalf("want rollback err, got %+v %v", res, err)
	}
	got := h.get("p1")
	if got.Activity != pet.ActivityIdle && got.Activity != "" {
		t.Fatalf("activity = %q, want idle", got.Activity)
	}
	if got.Stats.Energy != 80 {
		t.Fatalf("energy = %v, want 80 (StatsDelta rolled back)", got.Stats.Energy)
	}
}

func TestConcurrentApplyOnce(t *testing.T) {
	p := pet.New("p1", "团团", "cat", t0)
	p.Stats.Energy = 90
	h := newMemHost(p)
	m := New(h)
	_ = m.RegisterKind(ActivityKind{Name: pet.ActivityAdventuring, Owner: "adventure"})

	var commits int
	var mu sync.Mutex
	var wg sync.WaitGroup
	okN := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := m.Apply(context.Background(), "p1", Transition{
				To: pet.ActivityAdventuring, Owner: "adventure",
				StatsDelta: pet.Stats{Energy: -15},
				OnCommit: func(context.Context, Snapshot, Snapshot) error {
					mu.Lock()
					commits++
					mu.Unlock()
					return nil
				},
			})
			if err == nil && res.OK {
				mu.Lock()
				okN++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if commits != 1 || okN != 1 {
		t.Fatalf("commits=%d ok=%d, want 1/1", commits, okN)
	}
	if e := h.get("p1").Stats.Energy; e != 75 {
		t.Fatalf("energy = %v, want 75 (deducted once)", e)
	}
}

func TestAlreadyErrors(t *testing.T) {
	p := pet.New("p1", "团团", "cat", t0)
	h := newMemHost(p)
	m := New(h)
	_, _ = m.RequestSleep(context.Background(), "p1", "")
	res, err := m.RequestSleep(context.Background(), "p1", "")
	if err != nil || res.OK || !errors.Is(res.Err, pet.ErrAlready) {
		t.Fatalf("want already, got %+v %v", res, err)
	}
}

func TestGoIdleConsumesIntentSleepEvenIfEnergyHigh(t *testing.T) {
	p := pet.New("p1", "团团", "cat", t0)
	p.Stats.Energy = 90
	h := newMemHost(p)
	m := New(h)
	_ = m.RegisterKind(ActivityKind{Name: pet.ActivityAdventuring, Owner: "adventure"})
	_, err := m.Apply(context.Background(), "p1", Transition{
		To: pet.ActivityAdventuring, Owner: "adventure",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.RequestSleep(context.Background(), "p1", "autosleep")
	if err != nil || !res.Queued {
		t.Fatalf("want queued sleep, got %+v %v", res, err)
	}
	res, err = m.GoIdle(context.Background(), "p1", "finish")
	if err != nil || !res.OK {
		t.Fatalf("go idle = %+v %v", res, err)
	}
	got := h.get("p1")
	if got.Activity != pet.ActivitySleeping || !got.Sleeping {
		t.Fatalf("want sleeping after intent consume, got act=%q sleeping=%v", got.Activity, got.Sleeping)
	}
	if got.HasIntent(pet.IntentSleep) {
		t.Fatal("IntentSleep should be consumed")
	}
}
