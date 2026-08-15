package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/llm"
	"github.com/lalolv/PocketPet/internal/pet"
	"github.com/lalolv/PocketPet/internal/petfs"
	"github.com/lalolv/PocketPet/internal/store"
	"github.com/lalolv/PocketPet/internal/tick"
)

// activitiesSetup 搭一套带真实事件流水的 PetAgent（EventLister 接 store.RecentEvents）。
func activitiesSetup(t *testing.T) (*PetAgent, *store.Store, string) {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fs := petfs.New(t.TempDir())
	engine := tick.NewEngine(st, nil, time.Minute, 24*time.Hour, pet.NewFakeClock(time.Now()))
	ag := New(engine, fs, llm.Config{}, Options{EventLister: st.RecentEvents})
	p, err := engine.CreatePet(context.Background(), "kin", "cat")
	if err != nil {
		t.Fatal(err)
	}
	return ag, st, p.ID
}

func appendEvent(t *testing.T, st *store.Store, petID, typ, msg string) {
	t.Helper()
	_, err := st.AppendEvent(context.Background(), pet.Event{
		PetID: petID, Type: typ, Message: msg, CreatedAt: time.Now(),
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestRecentActivitiesFilter 验证活动记录的过滤与格式：噪声事件（身体预警、
// 梦境独白、genesis 系统载荷、空消息）不出现，经历类事件按时间排列。
func TestRecentActivitiesFilter(t *testing.T) {
	ag, st, id := activitiesSetup(t)
	appendEvent(t, st, id, "pet.adventure_finished", "kin 探险回来了，沿途发现了 1 个宝箱")
	appendEvent(t, st, id, pet.EventHungry, "kin 饿了")
	appendEvent(t, st, id, pet.EventDream, "梦见云朵")
	appendEvent(t, st, id, pet.EventGenesisNarration, "{\"text\":\"……\"}")
	appendEvent(t, st, id, pet.EventWokeUp, "kin 醒来了")
	appendEvent(t, st, id, "pet.adventure_moved", "")

	res, err := ag.recentActivities(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"探险回来了", "醒来了"} {
		if !strings.Contains(res.Activities, want) {
			t.Fatalf("activities missing %q:\n%s", want, res.Activities)
		}
	}
	for _, drop := range []string{"饿了", "梦见云朵", "……"} {
		if strings.Contains(res.Activities, drop) {
			t.Fatalf("activities should drop %q:\n%s", drop, res.Activities)
		}
	}
	// 每行带时间前缀（MM-dd HH:mm 格式，长度过短说明没带上时间）。
	for _, line := range strings.Split(res.Activities, "\n") {
		if len([]rune(line)) < 13 {
			t.Fatalf("line missing time prefix: %q", line)
		}
	}
}

// TestRecentActivitiesLimit 验证截取：过滤后只保留最近 activitiesShowMax 条。
func TestRecentActivitiesLimit(t *testing.T) {
	ag, st, id := activitiesSetup(t)
	for i := 0; i < activitiesShowMax+5; i++ {
		appendEvent(t, st, id, "pet.adventure_moved", fmt.Sprintf("kin 走到了【地点%02d】", i))
	}
	res, err := ag.recentActivities(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(res.Activities, "\n")
	if len(lines) != activitiesShowMax {
		t.Fatalf("lines = %d, want %d", len(lines), activitiesShowMax)
	}
	if !strings.Contains(lines[len(lines)-1], "地点24") {
		t.Fatalf("newest event should be kept:\n%s", res.Activities)
	}
	if strings.Contains(res.Activities, "地点00") {
		t.Fatalf("oldest events should be truncated:\n%s", res.Activities)
	}
}

// TestRecentActivitiesEmpty 验证无活动与未接线 EventLister 两种空态。
func TestRecentActivitiesEmpty(t *testing.T) {
	ag, _, id := activitiesSetup(t)
	res, err := ag.recentActivities(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	// 只有 born 事件时也有正常的活动记录文案。
	if !strings.Contains(res.Activities, "出生了") {
		t.Fatalf("unexpected first-day text: %q", res.Activities)
	}

	bare := New(ag.engine, ag.fs, llm.Config{})
	res, err = bare.recentActivities(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Activities, "还没有活动记录") {
		t.Fatalf("nil EventLister text: %q", res.Activities)
	}
	// 未接线 EventLister 时不注册该工具。
	tools, err := bare.buildTools(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, tl := range tools {
		if tl.Name() == "recent_activities" {
			t.Fatal("recent_activities should not be registered without EventLister")
		}
	}
}
