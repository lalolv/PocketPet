package pet

import (
	"math"
	"testing"
	"time"
)

func TestTraitsFromMap(t *testing.T) {
	tr := TraitsFromMap(map[string]float64{"playfulness": 0.9, "evil": 1, "appetite": 1.5})
	if tr.Playfulness != 0.9 || tr.Appetite != 1 || tr.Timidity != 0.5 {
		t.Fatalf("traits = %+v", tr)
	}
}

func TestHighAppetiteHungersFaster(t *testing.T) {
	hi := newTestPet()
	lo := newTestPet()
	hi.TickTraits(t0.Add(10*time.Hour), 0, Traits{Playfulness: 0.5, Timidity: 0.5, Appetite: 1, Sociability: 0.5})
	lo.TickTraits(t0.Add(10*time.Hour), 0, Traits{Playfulness: 0.5, Timidity: 0.5, Appetite: 0, Sociability: 0.5})
	if !(hi.Stats.Hunger < lo.Stats.Hunger) {
		t.Fatalf("high appetite hunger=%v, low=%v", hi.Stats.Hunger, lo.Stats.Hunger)
	}
}

func TestHighPlayfulnessPlayGainsMore(t *testing.T) {
	hi := newTestPet()
	lo := newTestPet()
	hi.Stats.Happy = 50
	lo.Stats.Happy = 50
	if _, err := hi.CareTraits(ActionPlay, t0, Traits{Playfulness: 1, Timidity: 0.5, Appetite: 0.5, Sociability: 0.5}); err != nil {
		t.Fatal(err)
	}
	if _, err := lo.CareTraits(ActionPlay, t0, Traits{Playfulness: 0, Timidity: 0.5, Appetite: 0.5, Sociability: 0.5}); err != nil {
		t.Fatal(err)
	}
	if !(hi.Stats.Happy > lo.Stats.Happy) {
		t.Fatalf("high play happy=%v, low=%v", hi.Stats.Happy, lo.Stats.Happy)
	}
	// 活泼耗能也更多
	if !(hi.Stats.Energy < lo.Stats.Energy) {
		t.Fatalf("high play energy=%v, low=%v", hi.Stats.Energy, lo.Stats.Energy)
	}
}

func TestHighAppetiteFeedGainsMore(t *testing.T) {
	hi := newTestPet()
	lo := newTestPet()
	hi.Stats.Hunger = 40
	lo.Stats.Hunger = 40
	if _, err := hi.CareTraits(ActionFeed, t0, Traits{Appetite: 1, Playfulness: 0.5, Timidity: 0.5, Sociability: 0.5}); err != nil {
		t.Fatal(err)
	}
	if _, err := lo.CareTraits(ActionFeed, t0, Traits{Appetite: 0, Playfulness: 0.5, Timidity: 0.5, Sociability: 0.5}); err != nil {
		t.Fatal(err)
	}
	if !(hi.Stats.Hunger > lo.Stats.Hunger) {
		t.Fatalf("high appetite after feed=%v, low=%v", hi.Stats.Hunger, lo.Stats.Hunger)
	}
}

func TestNeutralMatchesLegacyTick(t *testing.T) {
	a := newTestPet()
	b := newTestPet()
	a.Tick(t0.Add(5*time.Hour), 0)
	b.TickTraits(t0.Add(5*time.Hour), 0, NeutralTraits())
	if a.Stats != b.Stats {
		t.Fatalf("neutral diverged: %+v vs %+v", a.Stats, b.Stats)
	}
}

func TestMult(t *testing.T) {
	if math.Abs(mult(0.5, 0.25)-1) > 1e-9 {
		t.Fatal("mid should be 1")
	}
	if math.Abs(mult(1, 0.25)-1.25) > 1e-9 {
		t.Fatal("high")
	}
	if math.Abs(mult(0, 0.25)-0.75) > 1e-9 {
		t.Fatal("low")
	}
}
