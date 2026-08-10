// Package petfs 是宠物文件体系：每只宠物一个目录 <root>/pets/<pet-id>/，
// 人格与记忆以文件为准（source of truth），数值状态仍存 SQLite。
// 提供带锁（每只宠物一把）的读写 API。本包不 import ADK，也不依赖数据库。
package petfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// 宠物目录下的标准文件与目录名。
const (
	FilePET          = "PET.md"          // 身份卡
	FileSOUL         = "SOUL.md"         // 性格
	FileInstructions = "INSTRUCTIONS.md" // 行为准则
	FileAgent        = "AGENT.md"        // 装配声明（provider/model）
	FileMemory       = "MEMORY.md"       // 长期记忆
	DirMemory        = "memory"          // 日记目录（YYYY-MM-DD.md）
	DirSkills        = "skills"          // 技能目录（M4 预留）
)

var (
	// ErrExists 表示宠物目录已存在（重复创建）。
	ErrExists = errors.New("petfs: pet directory already exists")
	// ErrInvalidID 表示 petID 含非法字符（防路径穿越）。
	ErrInvalidID = errors.New("petfs: invalid pet id")
	// ErrInvalidName 表示请求的文件名不在允许范围内。
	ErrInvalidName = errors.New("petfs: invalid file name")
)

// idPattern 限定 petID 字符集，杜绝路径穿越。
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// journalNamePattern 限定日记文件名格式。
var journalNamePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}\.md$`)

// writableFiles 是允许通过 Read/Write 访问的顶层文件白名单。
var writableFiles = map[string]bool{
	FilePET:          true,
	FileSOUL:         true,
	FileInstructions: true,
	FileAgent:        true,
	FileMemory:       true,
}

// FS 管理数据根目录下全部宠物的文件。
type FS struct {
	root string

	mu    sync.Mutex
	locks map[string]*sync.Mutex // 每只宠物一把锁
}

// New 创建文件体系，root 为数据根目录（如 "data"），宠物目录为 root/pets/<id>。
func New(root string) *FS {
	return &FS{root: root, locks: make(map[string]*sync.Mutex)}
}

// Root 返回数据根目录。
func (fs *FS) Root() string { return fs.root }

// PetDir 返回某只宠物的目录路径（不做 ID 校验，仅供展示/调试）。
func (fs *FS) PetDir(id string) string { return filepath.Join(fs.root, "pets", id) }

// lock 返回 petID 对应的锁的解锁函数；ID 非法时返回 ErrInvalidID。
func (fs *FS) lock(id string) (func(), error) {
	if !idPattern.MatchString(id) {
		return nil, ErrInvalidID
	}
	fs.mu.Lock()
	l, ok := fs.locks[id]
	if !ok {
		l = &sync.Mutex{}
		fs.locks[id] = l
	}
	fs.mu.Unlock()
	l.Lock()
	return l.Unlock, nil
}

// Exists 报告该宠物是否已有文件体系（以 PET.md 为准）。
func (fs *FS) Exists(id string) bool {
	_, err := os.Stat(filepath.Join(fs.PetDir(id), FilePET))
	return err == nil
}

// Identity 是创建宠物文件所需的身份信息。
type Identity struct {
	Name        string    // 名字
	Species     string    // 物种
	Personality string    // 性格模板名（键或中文名），空 = 随机
	Master      string    // 对主人的称呼，空 = "主人"
	Stage       string    // 初始成长阶段，空 = "egg"
	BornAt      time.Time // 生日
}

// CreatePet 为宠物创建整套模板文件，返回实际使用的性格模板。
// 目录已存在（PET.md 已生成）时返回 ErrExists，不会覆盖已有文件。
func (fs *FS) CreatePet(id string, iden Identity) (Personality, error) {
	per, err := ResolvePersonality(iden.Personality)
	if err != nil {
		return Personality{}, err
	}
	if iden.Master == "" {
		iden.Master = "主人"
	}
	if iden.Stage == "" {
		iden.Stage = "egg"
	}

	unlock, err := fs.lock(id)
	if err != nil {
		return Personality{}, err
	}
	defer unlock()

	dir := fs.PetDir(id)
	if fs.Exists(id) {
		return Personality{}, ErrExists
	}
	for _, d := range []string{dir, filepath.Join(dir, DirMemory), filepath.Join(dir, DirSkills)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return Personality{}, fmt.Errorf("create pet dir: %w", err)
		}
	}
	files := map[string]string{
		FilePET:          petMD(iden),
		FileSOUL:         per.Soul,
		FileInstructions: instructionsMD(),
		FileAgent:        agentMD(),
		FileMemory:       memoryMD(),
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			return Personality{}, fmt.Errorf("write %s: %w", name, err)
		}
	}
	return per, nil
}

// Read 读取宠物的某个顶层文件（白名单内）。
func (fs *FS) Read(id, name string) (string, error) {
	if !writableFiles[name] {
		return "", ErrInvalidName
	}
	unlock, err := fs.lock(id)
	if err != nil {
		return "", err
	}
	defer unlock()
	b, err := os.ReadFile(filepath.Join(fs.PetDir(id), name))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// Write 覆盖写入宠物的某个顶层文件（白名单内）。
func (fs *FS) Write(id, name, content string) error {
	if !writableFiles[name] {
		return ErrInvalidName
	}
	unlock, err := fs.lock(id)
	if err != nil {
		return err
	}
	defer unlock()
	return os.WriteFile(filepath.Join(fs.PetDir(id), name), []byte(content), 0o644)
}

// UpdateStage 更新 PET.md frontmatter 中的 stage 字段（成长阶段晋升时调用），
// 其余内容（包括主人可能编辑过的正文）原样保留。
func (fs *FS) UpdateStage(id, stage string) error {
	unlock, err := fs.lock(id)
	if err != nil {
		return err
	}
	defer unlock()

	path := filepath.Join(fs.PetDir(id), FilePET)
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines, body, ok := splitFrontmatter(string(b))
	if !ok {
		return fmt.Errorf("petfs: %s has no frontmatter", FilePET)
	}
	found := false
	for i, ln := range lines {
		if strings.HasPrefix(ln, "stage:") {
			lines[i] = "stage: " + stage
			found = true
		}
	}
	if !found {
		lines = append(lines, "stage: "+stage)
	}
	out := "---\n" + strings.Join(lines, "\n") + "\n---\n" + body
	return os.WriteFile(path, []byte(out), 0o644)
}

// SoulTemplate 返回 SOUL.md frontmatter 中记录的性格模板键（如 "tsundere"）。
// 文件缺失或无该字段时返回空串。
func (fs *FS) SoulTemplate(id string) (string, error) {
	content, err := fs.Read(id, FileSOUL)
	if err != nil {
		return "", err
	}
	lines, _, ok := splitFrontmatter(content)
	if !ok {
		return "", nil
	}
	return frontmatterValue(lines, "template"), nil
}

// AgentSpec 是 AGENT.md frontmatter 的装配声明。
type AgentSpec struct {
	Model string // 空 = 跟随全局配置
	// MCPServers 是该宠物启用的 MCP server 名字列表（对应全局 POCKETPET_MCP_SERVERS），
	// frontmatter 里以逗号分隔，如 mcp: weather,smart-home。
	MCPServers []string
}

// AgentSpec 解析 AGENT.md frontmatter 的装配声明；留空字段返回零值。
func (fs *FS) AgentSpec(id string) (AgentSpec, error) {
	var spec AgentSpec
	content, err := fs.Read(id, FileAgent)
	if err != nil {
		return spec, err
	}
	lines, _, ok := splitFrontmatter(content)
	if !ok {
		return spec, nil
	}
	spec.Model = frontmatterValue(lines, "model")
	for _, name := range strings.Split(frontmatterValue(lines, "mcp"), ",") {
		if name = strings.TrimSpace(name); name != "" {
			spec.MCPServers = append(spec.MCPServers, name)
		}
	}
	return spec, nil
}

// AppendJournal 把一条记录追加到当天日记 memory/YYYY-MM-DD.md（不存在则新建并写入标题）。
func (fs *FS) AppendJournal(id, text string, now time.Time) error {
	unlock, err := fs.lock(id)
	if err != nil {
		return err
	}
	defer unlock()

	name := now.Format("2006-01-02") + ".md"
	path := filepath.Join(fs.PetDir(id), DirMemory, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		if _, err := fmt.Fprintf(f, "# %s 日记\n\n", now.Format("2006-01-02")); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(f, "- %s %s\n", now.Format("15:04"), text)
	return err
}

// ListJournals 返回全部日记文件名，按日期升序（最旧在前）。
func (fs *FS) ListJournals(id string) ([]string, error) {
	unlock, err := fs.lock(id)
	if err != nil {
		return nil, err
	}
	defer unlock()

	entries, err := os.ReadDir(filepath.Join(fs.PetDir(id), DirMemory))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && journalNamePattern.MatchString(e.Name()) {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// ReadJournal 读取一篇日记的内容。
func (fs *FS) ReadJournal(id, name string) (string, error) {
	if !journalNamePattern.MatchString(name) {
		return "", ErrInvalidName
	}
	unlock, err := fs.lock(id)
	if err != nil {
		return "", err
	}
	defer unlock()
	b, err := os.ReadFile(filepath.Join(fs.PetDir(id), DirMemory, name))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// splitFrontmatter 把 "---\n<fm>\n---\n<body>" 拆成 frontmatter 各行与正文。
// 无 frontmatter 时 ok=false，body 为原文。
func splitFrontmatter(content string) (lines []string, body string, ok bool) {
	const open = "---\n"
	if !strings.HasPrefix(content, open) {
		return nil, content, false
	}
	rest := content[len(open):]
	idx := strings.Index(rest, "\n---\n")
	if idx < 0 {
		return nil, content, false
	}
	return strings.Split(rest[:idx], "\n"), rest[idx+len("\n---\n"):], true
}

// frontmatterValue 取 frontmatter 顶层 "key: value" 的值（去掉首尾空白与引号）。
// 只匹配无缩进的行，嵌套结构（如 traits:）不受影响。
func frontmatterValue(lines []string, key string) string {
	prefix := key + ":"
	for _, ln := range lines {
		if strings.HasPrefix(ln, prefix) {
			v := strings.TrimSpace(ln[len(prefix):])
			return strings.Trim(v, `"'`)
		}
	}
	return ""
}
