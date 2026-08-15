package dream

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lalolv/PocketPet/internal/pet"
)

// TestOrganizeWritesDiary 验证入睡整理时的日记环节：
// gather 把当日活动记录（过滤噪声与昨日事件）喂给 Reflector，
// LLM 产出的 diary 落成当天日记条目，睡醒便签提及。
func TestOrganizeWritesDiary(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "lively")

	ctx := context.Background()
	evs := []pet.Event{
		// 今天的经历：应进入 TodayEvents。
		{PetID: p.ID, Type: "pet.adventure_finished", Message: "雪球 探险回来了，沿途发现了 1 个宝箱", CreatedAt: t0.Add(-2 * time.Hour)},
		// 身体预警：噪声，应过滤。
		{PetID: p.ID, Type: pet.EventHungry, Message: "雪球 饿了", CreatedAt: t0.Add(-time.Hour)},
		// 昨天的经历：应被当日边界排除。
		{PetID: p.ID, Type: pet.EventWokeUp, Message: "雪球 醒来了", CreatedAt: t0.Add(-26 * time.Hour)},
	}
	for _, e := range evs {
		if _, err := env.st.AppendEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}

	fake := &fakeReflector{res: ReflectResult{Diary: "今天去探险了，找到一个大宝箱，好开心。"}}
	env.org.Reflector = fake
	if err := env.org.Organize(ctx, p.ID); err != nil {
		t.Fatal(err)
	}

	// gather 输入：今日活动记录含经历、不含噪声与昨日事件。
	joined := strings.Join(fake.lastReq.TodayEvents, "\n")
	if !strings.Contains(joined, "探险回来了") {
		t.Fatalf("TodayEvents missing adventure:\n%s", joined)
	}
	if strings.Contains(joined, "饿了") {
		t.Fatalf("TodayEvents should drop stat warnings:\n%s", joined)
	}
	if strings.Contains(joined, "醒来了") {
		t.Fatalf("TodayEvents should drop yesterday's events:\n%s", joined)
	}

	// 日记落盘（当天唯一一篇）。
	journals, err := env.fs.ListJournals(p.ID)
	if err != nil || len(journals) != 1 {
		t.Fatalf("journals = %v, %v", journals, err)
	}
	journal, _ := env.fs.ReadJournal(p.ID, journals[0])
	if !strings.Contains(journal, "今天去探险了") {
		t.Fatalf("diary not in journal:\n%s", journal)
	}

	// 睡醒便签提及写日记。
	note, err := env.fs.TakeWakeNote(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "写进了日记") {
		t.Fatalf("wake note should mention diary:\n%s", note)
	}

	// 事件流里出现 diary_written，message 含日记内容（供 TUI 日志流展示）。
	types := env.eventTypes()
	if len(types) != 1 || types[0] != pet.EventDiaryWritten {
		t.Fatalf("events = %v, want [pet.diary_written]", types)
	}
	env.mu.Lock()
	msg := env.events[0].Message
	env.mu.Unlock()
	if !strings.Contains(msg, "写了日记：今天去探险了") {
		t.Fatalf("diary event message: %q", msg)
	}
}

// TestOrganizeSkipsDiary 验证 LLM 判断今天不值得记（diary 空）时不落日记。
func TestOrganizeSkipsDiary(t *testing.T) {
	env := setup(t)
	p := env.newPet(t, "quiet")

	env.org.Reflector = &fakeReflector{res: ReflectResult{Dream: "梦见星星。"}}
	if err := env.org.Organize(context.Background(), p.ID); err != nil {
		t.Fatal(err)
	}

	journals, _ := env.fs.ListJournals(p.ID)
	// 只有做梦条目，没有日记条目。
	if len(journals) != 1 {
		t.Fatalf("journals = %v, want 1", journals)
	}
	journal, _ := env.fs.ReadJournal(p.ID, journals[0])
	if strings.Contains(journal, "日记") && strings.Contains(journal, "探险") {
		t.Fatalf("unexpected diary entry:\n%s", journal)
	}
	note, _ := env.fs.TakeWakeNote(p.ID)
	if strings.Contains(note, "写进了日记") {
		t.Fatalf("wake note should not mention diary:\n%s", note)
	}
}

// TestParseReflectResultDiary 验证整理输出契约里的 diary 字段解析。
func TestParseReflectResultDiary(t *testing.T) {
	res, err := parseReflectResult(`{"memory_update":"","dream":"梦见云","diary":"今天很开心。"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res.Diary != "今天很开心。" {
		t.Fatalf("Diary = %q", res.Diary)
	}
}
