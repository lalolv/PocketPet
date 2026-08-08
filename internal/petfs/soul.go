package petfs

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DirSoulHistory 是 SOUL.md 演化历史目录名（宠物目录下）。
const DirSoulHistory = "SOUL.history"

// SoulDoc 是 SOUL.md 的解析结果：frontmatter 结构化字段 + 叙事正文。
// 解析是尽力而为的——手写/缺字段的文件也能读出零值字段，不报错。
type SoulDoc struct {
	Template string             // 性格模板键（如 "tsundere"），手写 SOUL 可能为空
	Label    string             // 中文名
	Locked   bool               // 锁定后禁止演化
	Traits   map[string]float64 // 特质权重（frontmatter traits: 下的缩进项）
	Body     string             // 叙事正文
}

// ParseSoul 解析 SOUL.md 内容。只认识顶层 key 与 traits: 下两空格缩进的
// "key: float" 行，其余行原样忽略（渲染时不会保留未知行——见 RenderSoul 注释）。
func ParseSoul(content string) SoulDoc {
	doc := SoulDoc{Traits: map[string]float64{}}
	lines, body, ok := splitFrontmatter(content)
	doc.Body = body
	if !ok {
		doc.Body = content
		return doc
	}
	inTraits := false
	for _, ln := range lines {
		switch {
		case ln == "traits:":
			inTraits = true
		case strings.HasPrefix(ln, " ") || strings.HasPrefix(ln, "\t"):
			// 缩进行：traits 下的特质权重
			if inTraits {
				if k, v, ok := strings.Cut(strings.TrimSpace(ln), ":"); ok {
					if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
						doc.Traits[strings.TrimSpace(k)] = f
					}
				}
			}
		default:
			inTraits = false
			switch {
			case strings.HasPrefix(ln, "template:"):
				doc.Template = frontmatterValue(lines, "template")
			case strings.HasPrefix(ln, "label:"):
				doc.Label = frontmatterValue(lines, "label")
			case strings.HasPrefix(ln, "locked:"):
				doc.Locked = frontmatterValue(lines, "locked") == "true"
			}
		}
	}
	return doc
}

// RenderSoul 把 SoulDoc 渲染回 SOUL.md 文本。
// 注意：frontmatter 只输出已知字段（template/label/locked/traits），
// 用户在 frontmatter 里手写的未知字段在演化重写时会丢失（M3 接受此限制）。
func RenderSoul(d SoulDoc) string {
	var b strings.Builder
	b.WriteString("---\n")
	if d.Template != "" {
		fmt.Fprintf(&b, "template: %s\n", d.Template)
	}
	if d.Label != "" {
		fmt.Fprintf(&b, "label: %s\n", d.Label)
	}
	fmt.Fprintf(&b, "locked: %t\n", d.Locked)
	if len(d.Traits) > 0 {
		b.WriteString("traits:\n")
		keys := make([]string, 0, len(d.Traits))
		for k := range d.Traits {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s: %s\n", k, strconv.FormatFloat(d.Traits[k], 'f', 2, 64))
		}
	}
	b.WriteString("---\n")
	body := d.Body
	if body != "" && !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	b.WriteString(body)
	return b.String()
}

// ReadSoulDoc 读取并解析宠物的 SOUL.md。
func (fs *FS) ReadSoulDoc(id string) (SoulDoc, error) {
	content, err := fs.Read(id, FileSOUL)
	if err != nil {
		return SoulDoc{}, err
	}
	return ParseSoul(content), nil
}

// SoulLocked 报告 SOUL 是否被锁定（文件缺失或无字段时为 false）。
func (fs *FS) SoulLocked(id string) bool {
	doc, err := fs.ReadSoulDoc(id)
	return err == nil && doc.Locked
}

// SetSoulLocked 改写 SOUL.md frontmatter 的 locked 字段（其余内容保留）。
// SOUL.md 没有 frontmatter 时补一个最小的。
func (fs *FS) SetSoulLocked(id string, locked bool) error {
	unlock, err := fs.lock(id)
	if err != nil {
		return err
	}
	defer unlock()

	path := filepath.Join(fs.PetDir(id), FileSOUL)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	value := "false"
	if locked {
		value = "true"
	}
	lines, body, ok := splitFrontmatter(string(b))
	if !ok {
		lines = nil
		body = string(b)
	}
	found := false
	for i, ln := range lines {
		if strings.HasPrefix(ln, "locked:") {
			lines[i] = "locked: " + value
			found = true
		}
	}
	if !found {
		lines = append(lines, "locked: "+value)
	}
	out := "---\n" + strings.Join(lines, "\n") + "\n---\n" + body
	return os.WriteFile(path, []byte(out), 0o644)
}

// WriteSoulWithHistory 写入新版 SOUL.md，并先把旧版本归档到
// SOUL.history/<timestamp>.md（毫秒级时间戳文件名，按字典序即时间序）。
// 当前没有 SOUL.md 时直接写入，不产生历史。
func (fs *FS) WriteSoulWithHistory(id, content string, now time.Time) error {
	unlock, err := fs.lock(id)
	if err != nil {
		return err
	}
	defer unlock()

	dir := fs.PetDir(id)
	old, err := os.ReadFile(filepath.Join(dir, FileSOUL))
	if err == nil {
		histDir := filepath.Join(dir, DirSoulHistory)
		if err := os.MkdirAll(histDir, 0o755); err != nil {
			return err
		}
		name := now.Format("20060102-150405") + fmt.Sprintf("-%03d", now.Nanosecond()/1e6) + ".md"
		if err := os.WriteFile(filepath.Join(histDir, name), old, 0o644); err != nil {
			return fmt.Errorf("archive soul: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.WriteFile(filepath.Join(dir, FileSOUL), []byte(content), 0o644)
}

// SoulHistory 返回演化历史文件名列表（升序，最旧在前）。
func (fs *FS) SoulHistory(id string) ([]string, error) {
	unlock, err := fs.lock(id)
	if err != nil {
		return nil, err
	}
	defer unlock()

	entries, err := os.ReadDir(filepath.Join(fs.PetDir(id), DirSoulHistory))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}
