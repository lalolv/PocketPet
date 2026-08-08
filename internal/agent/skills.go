package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	adktool "google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/mcptoolset"
	"google.golang.org/adk/v2/tool/skilltoolset"
	"google.golang.org/adk/v2/tool/skilltoolset/skill"

	"pocketpet/internal/config"
	"pocketpet/internal/petfs"
)

// skillSetSource 实现 skill.Source：按优先级（宠物私有在前、全局在后）逐目录扫描，
// 同名去重。每次调用都实时读盘——这就是技能热加载：梦境沉淀或主人新放的技能，
// 下一次对话请求即出现在指令里，无需重建 agent。
//
// 与 ADK 的 fileSystemSource 不同，这里做坏技能隔离：单个目录名与 frontmatter
// 不匹配、frontmatter 非法等问题只跳过该技能，不让整个列表报错拖垮对话。
type skillSetSource struct {
	roots []string
}

var _ skill.Source = skillSetSource{}

// ListFrontmatters 实现 skill.Source。
func (s skillSetSource) ListFrontmatters(ctx context.Context) ([]*skill.Frontmatter, error) {
	seen := make(map[string]bool)
	var out []*skill.Frontmatter
	for _, root := range s.roots {
		entries, err := os.ReadDir(root)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		src := skill.NewFileSystemSource(os.DirFS(root))
		for _, e := range entries {
			if !e.IsDir() || seen[e.Name()] {
				continue
			}
			fm, err := src.LoadFrontmatter(ctx, e.Name())
			if err != nil {
				continue // 坏技能跳过
			}
			seen[e.Name()] = true
			out = append(out, fm)
		}
	}
	return out, nil
}

// sourceFor 找到第一个含合法技能 name 的目录并返回其 Source。
func (s skillSetSource) sourceFor(ctx context.Context, name string) (skill.Source, error) {
	for _, root := range s.roots {
		fi, err := os.Stat(filepath.Join(root, name, "SKILL.md"))
		if err != nil || fi.IsDir() {
			continue
		}
		src := skill.NewFileSystemSource(os.DirFS(root))
		if _, err := src.LoadFrontmatter(ctx, name); err != nil {
			continue // 目录存在但技能非法：让位给低优先级目录的同名技能
		}
		return src, nil
	}
	return nil, fmt.Errorf("%w: %q", skill.ErrSkillNotFound, name)
}

// LoadFrontmatter 实现 skill.Source。
func (s skillSetSource) LoadFrontmatter(ctx context.Context, name string) (*skill.Frontmatter, error) {
	src, err := s.sourceFor(ctx, name)
	if err != nil {
		return nil, err
	}
	return src.LoadFrontmatter(ctx, name)
}

// LoadInstructions 实现 skill.Source。
func (s skillSetSource) LoadInstructions(ctx context.Context, name string) (string, error) {
	src, err := s.sourceFor(ctx, name)
	if err != nil {
		return "", err
	}
	return src.LoadInstructions(ctx, name)
}

// LoadResource 实现 skill.Source。
func (s skillSetSource) LoadResource(ctx context.Context, name, resourcePath string) (io.ReadCloser, error) {
	src, err := s.sourceFor(ctx, name)
	if err != nil {
		return nil, err
	}
	return src.LoadResource(ctx, name, resourcePath)
}

// ListResources 实现 skill.Source。
func (s skillSetSource) ListResources(ctx context.Context, name, subpath string) ([]string, error) {
	src, err := s.sourceFor(ctx, name)
	if err != nil {
		return nil, err
	}
	return src.ListResources(ctx, name, subpath)
}

// buildToolsets 装配宠物的工具集：技能（私有 + 全局）始终挂载（无技能时
// skilltoolset 不在请求里注入任何内容，代价只是三个固定工具声明）；
// MCP 按 AGENT.md 声明的名字挂载，未声明/连不上的只告警不阻断。
func (a *PetAgent) buildToolsets(ctx context.Context, petID string, mcpNames []string) ([]adktool.Toolset, error) {
	var tss []adktool.Toolset

	roots := []string{filepath.Join(a.fs.PetDir(petID), petfs.DirSkills)}
	if a.opts.SkillsDir != "" {
		roots = append(roots, a.opts.SkillsDir)
	}
	sts, err := skilltoolset.New(ctx, skilltoolset.Config{Source: skillSetSource{roots: roots}})
	if err != nil {
		return nil, fmt.Errorf("skill toolset: %w", err)
	}
	tss = append(tss, sts)

	for _, name := range mcpNames {
		spec, ok := a.mcpServer(name)
		if !ok {
			slog.Warn("agent: AGENT.md enables undeclared MCP server, skip", "pet", petID, "name", name)
			continue
		}
		mkTransport := a.opts.MCPTransport
		if mkTransport == nil {
			mkTransport = defaultMCPTransport
		}
		tr, err := mkTransport(spec)
		if err != nil {
			slog.Warn("agent: build MCP transport failed, skip", "pet", petID, "name", name, "err", err)
			continue
		}
		ts, err := mcptoolset.New(mcptoolset.Config{Transport: tr})
		if err != nil {
			slog.Warn("agent: create MCP toolset failed, skip", "pet", petID, "name", name, "err", err)
			continue
		}
		tss = append(tss, ts)
	}
	return tss, nil
}

// mcpServer 按名字查全局声明。
func (a *PetAgent) mcpServer(name string) (config.MCPServer, bool) {
	for _, s := range a.opts.MCPServers {
		if s.Name == name {
			return s, true
		}
	}
	return config.MCPServer{}, false
}

// defaultMCPTransport 是生产传输：stdio CommandTransport。
// 继承当前进程环境并叠加 spec.Env。
func defaultMCPTransport(spec config.MCPServer) (mcp.Transport, error) {
	if spec.Command == "" {
		return nil, errors.New("mcp server command is empty")
	}
	cmd := exec.Command(spec.Command, spec.Args...)
	env := os.Environ()
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	return &mcp.CommandTransport{Command: cmd}, nil
}

// SkillInfo 是宠物可见技能（私有 + 全局）的展示信息。
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Source      string `json:"source"` // learned（梦境沉淀）| custom（私有手工）| global（全局）
}

// SkillsFor 列出宠物可见的全部技能：私有优先，全局同名去重。
func (a *PetAgent) SkillsFor(petID string) ([]SkillInfo, error) {
	priv, err := a.fs.ListSkillMetas(petID)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	out := make([]SkillInfo, 0, len(priv))
	for _, m := range priv {
		seen[m.Name] = true
		out = append(out, SkillInfo{Name: m.Name, Description: m.Description, Source: m.Origin})
	}
	if a.opts.SkillsDir != "" {
		glob, err := petfs.ListSkillsInDir(a.opts.SkillsDir)
		if err != nil {
			return nil, err
		}
		for _, m := range glob {
			if !seen[m.Name] {
				out = append(out, SkillInfo{Name: m.Name, Description: m.Description, Source: "global"})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
