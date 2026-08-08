package petfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var bornAt = time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)

func testFS(t *testing.T) *FS {
	t.Helper()
	return New(filepath.Join(t.TempDir(), "data"))
}

func TestCreatePetFiles(t *testing.T) {
	fs := testFS(t)
	per, err := fs.CreatePet("pet1", Identity{
		Name: "团团", Species: "cat", Personality: "傲娇", Stage: "egg", BornAt: bornAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if per.Key != "tsundere" {
		t.Fatalf("personality = %q", per.Key)
	}

	dir := fs.PetDir("pet1")
	for _, name := range []string{FilePET, FileSOUL, FileInstructions, FileAgent, FileMemory} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	for _, sub := range []string{DirMemory, DirSkills} {
		if st, err := os.Stat(filepath.Join(dir, sub)); err != nil || !st.IsDir() {
			t.Fatalf("missing dir %s: %v", sub, err)
		}
	}

	petMD, _ := fs.Read("pet1", FilePET)
	for _, want := range []string{"name: 团团", "species: cat", "stage: egg", "master: 主人", "born_at:"} {
		if !strings.Contains(petMD, want) {
			t.Fatalf("PET.md missing %q:\n%s", want, petMD)
		}
	}
	soul, _ := fs.Read("pet1", FileSOUL)
	if !strings.Contains(soul, "template: tsundere") || !strings.Contains(soul, "traits:") {
		t.Fatalf("SOUL.md unexpected:\n%s", soul)
	}
	if tpl, _ := fs.SoulTemplate("pet1"); tpl != "tsundere" {
		t.Fatalf("SoulTemplate = %q", tpl)
	}
	ins, _ := fs.Read("pet1", FileInstructions)
	if !strings.Contains(ins, "第一人称") {
		t.Fatalf("INSTRUCTIONS.md unexpected:\n%s", ins)
	}
}

func TestPersonalityResolution(t *testing.T) {
	// 中文别名
	p, err := ResolvePersonality("傲娇")
	if err != nil || p.Key != "tsundere" {
		t.Fatalf("ResolvePersonality(傲娇) = %v, %v", p.Key, err)
	}
	// 规范键
	if p, err = ResolvePersonality("lively"); err != nil || p.Label != "活泼" {
		t.Fatalf("ResolvePersonality(lively) = %v, %v", p.Label, err)
	}
	// 空 = 随机（结果必须是已知模板之一）
	p, err = ResolvePersonality("")
	if err != nil {
		t.Fatal(err)
	}
	known := false
	for _, k := range PersonalityKeys() {
		if p.Key == k {
			known = true
		}
	}
	if !known {
		t.Fatalf("random personality %q not in %v", p.Key, PersonalityKeys())
	}
	// 未知模板报错
	if _, err = ResolvePersonality("暴躁"); !errors.Is(err, ErrUnknownPersonality) {
		t.Fatalf("unknown = %v", err)
	}
}

func TestCreateExistingFails(t *testing.T) {
	fs := testFS(t)
	if _, err := fs.CreatePet("pet1", Identity{Name: "a", Species: "cat"}); err != nil {
		t.Fatal(err)
	}
	if _, err := fs.CreatePet("pet1", Identity{Name: "b", Species: "dog"}); !errors.Is(err, ErrExists) {
		t.Fatalf("re-create = %v, want ErrExists", err)
	}
	// 原文件未被覆盖
	petMD, _ := fs.Read("pet1", FilePET)
	if !strings.Contains(petMD, "name: a") {
		t.Fatalf("PET.md overwritten:\n%s", petMD)
	}
}

func TestUpdateStage(t *testing.T) {
	fs := testFS(t)
	if _, err := fs.CreatePet("pet1", Identity{Name: "团团", Species: "cat", BornAt: bornAt}); err != nil {
		t.Fatal(err)
	}
	before, _ := fs.Read("pet1", FilePET)
	if !strings.Contains(before, "stage: egg") {
		t.Fatalf("initial stage:\n%s", before)
	}

	if err := fs.UpdateStage("pet1", "baby"); err != nil {
		t.Fatal(err)
	}
	after, _ := fs.Read("pet1", FilePET)
	if !strings.Contains(after, "stage: baby") {
		t.Fatalf("stage not updated:\n%s", after)
	}
	// 其余字段与正文原样保留
	for _, want := range []string{"name: 团团", "我是团团"} {
		if !strings.Contains(after, want) {
			t.Fatalf("PET.md lost %q after UpdateStage:\n%s", want, after)
		}
	}
}

func TestJournal(t *testing.T) {
	fs := testFS(t)
	if _, err := fs.CreatePet("pet1", Identity{Name: "团团", Species: "cat"}); err != nil {
		t.Fatal(err)
	}
	day1 := time.Date(2026, 8, 8, 20, 0, 0, 0, time.Local)
	day2 := day1.AddDate(0, 0, 1)

	if err := fs.AppendJournal("pet1", "主人今天心情不好", day1); err != nil {
		t.Fatal(err)
	}
	if err := fs.AppendJournal("pet1", "主人给我起了新名字", day1); err != nil {
		t.Fatal(err)
	}
	if err := fs.AppendJournal("pet1", "第二天的事", day2); err != nil {
		t.Fatal(err)
	}

	list, err := fs.ListJournals("pet1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{day1.Format("2006-01-02") + ".md", day2.Format("2006-01-02") + ".md"}
	if len(list) != 2 || list[0] != want[0] || list[1] != want[1] {
		t.Fatalf("ListJournals = %v, want %v", list, want)
	}

	content, err := fs.ReadJournal("pet1", want[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(content, "# "+day1.Format("2006-01-02")+" 日记") ||
		!strings.Contains(content, "- 20:00 主人今天心情不好") ||
		!strings.Contains(content, "- 20:00 主人给我起了新名字") {
		t.Fatalf("journal entries:\n%s", content)
	}

	// 文件名 / ID 校验
	if _, err := fs.ReadJournal("pet1", "../../etc/passwd"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("traversal = %v", err)
	}
	if _, err := fs.Read("pet1", "../../etc/passwd"); !errors.Is(err, ErrInvalidName) {
		t.Fatalf("read traversal = %v", err)
	}
	if _, err := fs.Read("../escape", FilePET); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("bad id = %v", err)
	}
}

func TestAgentSpec(t *testing.T) {
	fs := testFS(t)
	if _, err := fs.CreatePet("pet1", Identity{Name: "团团", Species: "cat"}); err != nil {
		t.Fatal(err)
	}
	// 默认模板：全部留空
	spec, err := fs.AgentSpec("pet1")
	if err != nil || spec.Provider != "" || spec.Model != "" || len(spec.MCPServers) != 0 {
		t.Fatalf("default AgentSpec = %+v, %v", spec, err)
	}
	// 主人编辑后生效（含 mcp 声明）
	agentMD := "---\nprovider: openai-compatible\nmodel: \"gpt-4o-mini\"\nmcp: weather, smart-home\n---\n自定义说明\n"
	if err := fs.Write("pet1", FileAgent, agentMD); err != nil {
		t.Fatal(err)
	}
	spec, err = fs.AgentSpec("pet1")
	if err != nil || spec.Provider != "openai-compatible" || spec.Model != "gpt-4o-mini" {
		t.Fatalf("AgentSpec = %+v, %v", spec, err)
	}
	if len(spec.MCPServers) != 2 || spec.MCPServers[0] != "weather" || spec.MCPServers[1] != "smart-home" {
		t.Fatalf("mcp servers = %v", spec.MCPServers)
	}
}

// TestConcurrentReadWrite 并发追加日记 + 读取：每宠物一把锁保证不串数据。
func TestConcurrentReadWrite(t *testing.T) {
	fs := testFS(t)
	if _, err := fs.CreatePet("pet1", Identity{Name: "团团", Species: "cat"}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.Local)
	const n = 16
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := fs.AppendJournal("pet1", fmt.Sprintf("fact-%02d", i), now); err != nil {
				t.Error(err)
			}
			if _, err := fs.Read("pet1", FileSOUL); err != nil {
				t.Error(err)
			}
			if _, err := fs.ListJournals("pet1"); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()

	content, err := fs.ReadJournal("pet1", "2026-08-08.md")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if !strings.Contains(content, fmt.Sprintf("fact-%02d", i)) {
			t.Fatalf("journal missing fact-%02d:\n%s", i, content)
		}
	}
}

func TestExists(t *testing.T) {
	fs := testFS(t)
	if fs.Exists("pet1") {
		t.Fatal("exists before create")
	}
	if _, err := fs.CreatePet("pet1", Identity{Name: "a", Species: "cat"}); err != nil {
		t.Fatal(err)
	}
	if !fs.Exists("pet1") {
		t.Fatal("not exists after create")
	}
}
