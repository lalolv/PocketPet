package narrate

import (
	"strings"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
)

func TestSleepyQueuedForbidsAsleepSpeech(t *testing.T) {
	p := pet.New("p1", "Kin", "cat", time.Now())
	p.Activity = pet.ActivityAdventuring
	p.AddIntent(pet.IntentSleep)
	p.SyncSleepingFromActivity()

	ctx := FromPet(p)
	ctx.Effect = EffectQueuedSleep
	f := Policy(pet.EventSleepy, ctx)
	if !f.MaySpeak {
		t.Fatal("should speak")
	}
	if !strings.Contains(f.Fact, "探险") && !strings.Contains(f.Fact, "还没睡着") {
		t.Fatalf("fact = %q", f.Fact)
	}
	joined := strings.Join(f.Forbid, " ")
	for _, bad := range []string{"已经睡着了", "主人晚安", "去窝里睡了"} {
		if !strings.Contains(joined, bad) {
			t.Fatalf("Forbid missing %q: %v", bad, f.Forbid)
		}
	}
	block := f.PromptBlock()
	if strings.Contains(block, "马上要去睡觉了") {
		t.Fatalf("must not claim immediate sleep:\n%s", block)
	}
}

func TestSleepySleptAllowsGoodnight(t *testing.T) {
	p := pet.New("p1", "Kin", "cat", time.Now())
	p.Activity = pet.ActivitySleeping
	p.SyncSleepingFromActivity()
	ctx := FromPet(p)
	ctx.Effect = EffectSlept
	f := Policy(pet.EventSleepy, ctx)
	if !strings.Contains(f.Instruction, "晚安") {
		t.Fatalf("instruction = %q", f.Instruction)
	}
}

func TestHungryWhileAdventuring(t *testing.T) {
	p := pet.New("p1", "Kin", "cat", time.Now())
	p.Activity = pet.ActivityAdventuring
	p.SyncSleepingFromActivity()
	f := Policy(pet.EventHungry, FromPet(p))
	if !strings.Contains(f.Fact, "探险") {
		t.Fatalf("fact = %q", f.Fact)
	}
	if !strings.Contains(strings.Join(f.Forbid, " "), "睡觉") {
		t.Fatalf("forbid = %v", f.Forbid)
	}
}

func TestCareSleepQueuedOutcome(t *testing.T) {
	p := pet.New("p1", "Kin", "cat", time.Now())
	p.Activity = pet.ActivityAdventuring
	p.AddIntent(pet.IntentSleep)
	out := SleepBusyOutcome(p)
	if !strings.Contains(out, "忙完再睡") {
		t.Fatalf("outcome = %q", out)
	}
}

func TestChatConstraintBlocksPrematureSleep(t *testing.T) {
	p := pet.New("p1", "Kin", "cat", time.Now())
	p.Activity = pet.ActivityAdventuring
	p.AddIntent(pet.IntentSleep)
	s := ChatConstraint(p)
	if !strings.Contains(s, "还没睡着") {
		t.Fatalf("constraint = %q", s)
	}
}
