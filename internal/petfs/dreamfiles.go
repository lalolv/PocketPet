package petfs

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// FileWakeNote 是"睡醒便签"：梦境整理器在宠物入睡后写入（含梦境摘要），
// 宠物醒来后的第一次对话消费（读后删除）。放在宠物根目录，对主人可见。
const FileWakeNote = "WAKE.md"

// skillNamePattern 限定技能名为 kebab-case（与 ADK SKILL.md 目录名约定一致）。
var skillNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

// WriteSkill 把沉淀出的技能写入 skills/<name>/SKILL.md。
// name 非法或技能已存在时返回错误（已存在不覆盖：同一经验不重复沉淀）。
func (fs *FS) WriteSkill(id, name, content string) error {
	if !skillNamePattern.MatchString(name) || len(name) > 64 {
		return ErrInvalidName
	}
	unlock, err := fs.lock(id)
	if err != nil {
		return err
	}
	defer unlock()

	dir := filepath.Join(fs.PetDir(id), DirSkills, name)
	if _, err := os.Stat(filepath.Join(dir, "SKILL.md")); err == nil {
		return ErrExists
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644)
}

// ListSkills 返回已沉淀/安装的技能名列表（skills/ 下含 SKILL.md 的子目录名，升序）。
func (fs *FS) ListSkills(id string) ([]string, error) {
	unlock, err := fs.lock(id)
	if err != nil {
		return nil, err
	}
	defer unlock()

	entries, err := os.ReadDir(filepath.Join(fs.PetDir(id), DirSkills))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if _, err := os.Stat(filepath.Join(fs.PetDir(id), DirSkills, e.Name(), "SKILL.md")); err == nil {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// WriteWakeNote 写（覆盖）睡醒便签。
func (fs *FS) WriteWakeNote(id, text string) error {
	unlock, err := fs.lock(id)
	if err != nil {
		return err
	}
	defer unlock()
	return os.WriteFile(filepath.Join(fs.PetDir(id), FileWakeNote), []byte(text), 0o644)
}

// TakeWakeNote 读取睡醒便签并删除（消费语义）；没有便签时返回空串。
func (fs *FS) TakeWakeNote(id string) (string, error) {
	unlock, err := fs.lock(id)
	if err != nil {
		return "", err
	}
	defer unlock()
	path := filepath.Join(fs.PetDir(id), FileWakeNote)
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	// 读完后删除；删除失败不致命（下次会再看到同样的便签）。
	_ = os.Remove(path)
	return string(b), nil
}

// SkillMeta 是一个技能的展示元信息。
// Origin 区分来源："learned"（梦境沉淀，SKILL.md metadata 里标了 origin: learned）、
// "custom"（手工放入宠物私有目录）、"global"（全局技能目录）。
type SkillMeta struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Origin      string `json:"origin"`
}

// ListSkillMetas 返回宠物私有技能的元信息列表（持宠物锁；坏技能跳过）。
func (fs *FS) ListSkillMetas(id string) ([]SkillMeta, error) {
	unlock, err := fs.lock(id)
	if err != nil {
		return nil, err
	}
	defer unlock()
	return listSkillMetasIn(filepath.Join(fs.PetDir(id), DirSkills), "custom")
}

// ListSkillsInDir 列出一个技能目录（如全局 skills/）下的技能元信息。
// 目录不存在时返回空。Origin 字段由调用方语义决定（全局传 "global"）；
// 若 SKILL.md metadata 标了 origin: learned 则以标记为准。
func ListSkillsInDir(dir string) ([]SkillMeta, error) {
	return listSkillMetasIn(dir, "global")
}

func listSkillMetasIn(dir, defaultOrigin string) ([]SkillMeta, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []SkillMeta
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "SKILL.md"))
		if err != nil {
			continue
		}
		meta := parseSkillMeta(string(b))
		if meta.Name == "" {
			meta.Name = e.Name()
		}
		meta.Origin = defaultOrigin
		if o := skillOrigin(string(b)); o != "" {
			meta.Origin = o
		}
		out = append(out, meta)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// parseSkillMeta 从 SKILL.md 提取 name/description（frontmatter 顶层字段）。
func parseSkillMeta(content string) SkillMeta {
	var meta SkillMeta
	lines, _, ok := splitFrontmatter(content)
	if !ok {
		return meta
	}
	meta.Name = frontmatterValue(lines, "name")
	meta.Description = frontmatterValue(lines, "description")
	return meta
}

// skillOrigin 取 SKILL.md frontmatter 里 metadata: 下的 origin 标记。
func skillOrigin(content string) string {
	lines, _, ok := splitFrontmatter(content)
	if !ok {
		return ""
	}
	inMeta := false
	for _, ln := range lines {
		switch {
		case ln == "metadata:":
			inMeta = true
		case strings.HasPrefix(ln, " "):
			if inMeta {
				if k, v, ok := strings.Cut(strings.TrimSpace(ln), ":"); ok && strings.TrimSpace(k) == "origin" {
					return strings.Trim(strings.TrimSpace(v), `"'`)
				}
			}
		default:
			inMeta = false
		}
	}
	return ""
}
