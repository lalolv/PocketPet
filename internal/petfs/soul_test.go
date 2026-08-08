package petfs

import (
	"strings"
	"testing"
	"time"
)

func TestParseRenderSoulRoundTrip(t *testing.T) {
	fs := testFS(t)
	if _, err := fs.CreatePet("pet1", Identity{
		Name: "团团", Species: "cat", Personality: "tsundere", BornAt: bornAt,
	}); err != nil {
		t.Fatal(err)
	}

	raw, err := fs.Read("pet1", FileSOUL)
	if err != nil {
		t.Fatal(err)
	}
	doc := ParseSoul(raw)
	if doc.Template != "tsundere" || doc.Label != "傲娇" {
		t.Fatalf("template/label = %q/%q", doc.Template, doc.Label)
	}
	if doc.Locked {
		t.Fatal("new soul should not be locked")
	}
	if doc.Traits["playfulness"] != 0.6 || doc.Traits["timidity"] != 0.2 ||
		doc.Traits["appetite"] != 0.7 || doc.Traits["sociability"] != 0.4 {
		t.Fatalf("traits = %v", doc.Traits)
	}
	if !strings.Contains(doc.Body, "嘴硬心软") {
		t.Fatalf("body = %q", doc.Body)
	}

	// 渲染 → 再解析：字段保持
	doc.Locked = true
	doc.Traits["playfulness"] = 0.7
	doc2 := ParseSoul(RenderSoul(doc))
	if doc2.Template != "tsundere" || !doc2.Locked || doc2.Traits["playfulness"] != 0.7 {
		t.Fatalf("round trip = %+v", doc2)
	}
	if !strings.Contains(doc2.Body, "嘴硬心软") {
		t.Fatalf("body lost in round trip: %q", doc2.Body)
	}
}

func TestSetSoulLocked(t *testing.T) {
	fs := testFS(t)
	if _, err := fs.CreatePet("pet1", Identity{Name: "团团", Species: "cat", Personality: "lively"}); err != nil {
		t.Fatal(err)
	}
	if fs.SoulLocked("pet1") {
		t.Fatal("should start unlocked")
	}
	if err := fs.SetSoulLocked("pet1", true); err != nil {
		t.Fatal(err)
	}
	if !fs.SoulLocked("pet1") {
		t.Fatal("should be locked")
	}
	// 模板字段仍在
	if tpl, _ := fs.SoulTemplate("pet1"); tpl != "lively" {
		t.Fatalf("template lost after lock: %q", tpl)
	}
	if err := fs.SetSoulLocked("pet1", false); err != nil {
		t.Fatal(err)
	}
	if fs.SoulLocked("pet1") {
		t.Fatal("should be unlocked")
	}

	// 手写无 frontmatter 的 SOUL 也能锁定（补一个最小 frontmatter）
	if err := fs.Write("pet1", FileSOUL, "我是一只没有 frontmatter 的宠物。\n"); err != nil {
		t.Fatal(err)
	}
	if err := fs.SetSoulLocked("pet1", true); err != nil {
		t.Fatal(err)
	}
	if !fs.SoulLocked("pet1") {
		t.Fatal("plain-text soul should be lockable")
	}
	if content, _ := fs.Read("pet1", FileSOUL); !strings.Contains(content, "没有 frontmatter") {
		t.Fatal("body lost when adding frontmatter")
	}
}

func TestWriteSoulWithHistory(t *testing.T) {
	fs := testFS(t)
	if _, err := fs.CreatePet("pet1", Identity{Name: "团团", Species: "cat", Personality: "quiet"}); err != nil {
		t.Fatal(err)
	}
	old, _ := fs.Read("pet1", FileSOUL)

	ts1 := time.Date(2026, 8, 9, 3, 0, 0, 0, time.UTC)
	if err := fs.WriteSoulWithHistory("pet1", "---\ntemplate: quiet\n---\n新正文 v1\n", ts1); err != nil {
		t.Fatal(err)
	}
	hist, err := fs.SoulHistory("pet1")
	if err != nil || len(hist) != 1 {
		t.Fatalf("history = %v, %v", hist, err)
	}
	if !strings.HasPrefix(hist[0], "20260809-030000") {
		t.Fatalf("history name = %q", hist[0])
	}

	// 第二次演化：历史变两份且升序；第一份内容是最初的模板
	ts2 := ts1.Add(time.Hour)
	if err := fs.WriteSoulWithHistory("pet1", "---\ntemplate: quiet\n---\n新正文 v2\n", ts2); err != nil {
		t.Fatal(err)
	}
	hist, _ = fs.SoulHistory("pet1")
	if len(hist) != 2 || hist[0] >= hist[1] {
		t.Fatalf("history = %v", hist)
	}
	// 当前 SOUL 是 v2
	cur, _ := fs.Read("pet1", FileSOUL)
	if !strings.Contains(cur, "新正文 v2") {
		t.Fatalf("current soul:\n%s", cur)
	}
	_ = old
}

func TestSkillFiles(t *testing.T) {
	fs := testFS(t)
	if _, err := fs.CreatePet("pet1", Identity{Name: "团团", Species: "cat"}); err != nil {
		t.Fatal(err)
	}
	if err := fs.WriteSkill("pet1", "goodnight-ritual", "---\nname: goodnight-ritual\ndescription: 晚安\n---\n正文\n"); err != nil {
		t.Fatal(err)
	}
	// 重复写 → ErrExists
	if err := fs.WriteSkill("pet1", "goodnight-ritual", "x"); err == nil {
		t.Fatal("duplicate skill should fail")
	}
	// 非法名字
	if err := fs.WriteSkill("pet1", "Bad Name!", "x"); err == nil {
		t.Fatal("invalid skill name should fail")
	}
	skills, err := fs.ListSkills("pet1")
	if err != nil || len(skills) != 1 || skills[0] != "goodnight-ritual" {
		t.Fatalf("skills = %v, %v", skills, err)
	}
}

func TestWakeNote(t *testing.T) {
	fs := testFS(t)
	if _, err := fs.CreatePet("pet1", Identity{Name: "团团", Species: "cat"}); err != nil {
		t.Fatal(err)
	}
	// 没有便签 → 空串
	if note, err := fs.TakeWakeNote("pet1"); err != nil || note != "" {
		t.Fatalf("empty take = %q, %v", note, err)
	}
	if err := fs.WriteWakeNote("pet1", "你刚睡了一觉。\n"); err != nil {
		t.Fatal(err)
	}
	note, err := fs.TakeWakeNote("pet1")
	if err != nil || !strings.Contains(note, "睡了一觉") {
		t.Fatalf("take = %q, %v", note, err)
	}
	// 消费后即删
	if note, _ := fs.TakeWakeNote("pet1"); note != "" {
		t.Fatalf("note should be consumed, got %q", note)
	}
}
