package metaagent

import (
	"context"

	adkagent "google.golang.org/adk/v2/agent"
	adktool "google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/lalolv/PocketPet/internal/pet"
)

type narrateArgs struct {
	Stage string `json:"stage" jsonschema:"阶段名，如 genes/temperament/appearance/quirks/soul/stats/identity/finalize"`
	Text  string `json:"text" jsonschema:"1～2句旁白，有画面感，给玩家看"`
}

type rollGenesArgs struct {
	Traits map[string]float64 `json:"traits,omitempty" jsonschema:"可选特质微调；省略则按种子纯采样。键仅 playfulness/timidity/appetite/sociability，值0～1"`
}

type temperamentArgs struct {
	Label string `json:"label" jsonschema:"气质标签，不超过8个汉字，避免空洞词"`
	Blurb string `json:"blurb" jsonschema:"一句话气质说明"`
}

type appearanceArgs struct {
	Appearance string `json:"appearance" jsonschema:"外貌描述段落"`
}

type quirksArgs struct {
	Quirks []string `json:"quirks" jsonschema:"2～5条癖好，每条简短"`
}

type soulArgs struct {
	Narrative string `json:"narrative" jsonschema:"第一人称SOUL正文，约80～400字"`
}

type statsArgs struct {
	UseSuggested bool     `json:"use_suggested" jsonschema:"true表示接受系统按基因算出的建议值"`
	Hunger       *float64 `json:"hunger,omitempty" jsonschema:"饱食度50～90；use_suggested时可省略"`
	Happy        *float64 `json:"happy,omitempty" jsonschema:"心情55～95"`
	Clean        *float64 `json:"clean,omitempty" jsonschema:"清洁50～95"`
	Energy       *float64 `json:"energy,omitempty" jsonschema:"精力70～100"`
}

type identityArgs struct {
	Name          string `json:"name,omitempty" jsonschema:"名字；有预定名时优先用预定名"`
	Master        string `json:"master,omitempty" jsonschema:"对主人的称呼，默认主人"`
	SpeciesFlavor string `json:"species_flavor,omitempty" jsonschema:"物种风味描述，如橘白短毛猫"`
}

type noArgs struct{}

// buildADKTools 把 Workshop 包装为 MetaAgent 可调用的 ADK 工具。
func (w *Workshop) buildADKTools() ([]adktool.Tool, error) {
	var tools []adktool.Tool
	add := func(t adktool.Tool, err error) error {
		if err != nil {
			return err
		}
		tools = append(tools, t)
		return nil
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "narrate",
		Description: "向玩家播出诞生旁白（1～2句有画面感的描述）。每阶段至少调用一次。",
	}, func(_ adkagent.Context, args narrateArgs) (ToolResult, error) {
		w.Narrate(context.Background(), args.Stage, args.Text)
		return okResult("narrated", map[string]any{"stage": args.Stage}), nil
	})); err != nil {
		return nil, err
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "roll_genes",
		Description: "掷出/确认特质基因（playfulness/timidity/appetite/sociability）。可省略 traits 做种子采样。",
	}, func(_ adkagent.Context, args rollGenesArgs) (ToolResult, error) {
		return w.RollGenes(context.Background(), args.Traits), nil
	})); err != nil {
		return nil, err
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "set_temperament",
		Description: "设定气质标签与一句话说明。须在 roll_genes 之后。",
	}, func(_ adkagent.Context, args temperamentArgs) (ToolResult, error) {
		return w.SetTemperament(context.Background(), args.Label, args.Blurb), nil
	})); err != nil {
		return nil, err
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "set_appearance",
		Description: "写入外貌描述。须在 set_temperament 之后。",
	}, func(_ adkagent.Context, args appearanceArgs) (ToolResult, error) {
		return w.SetAppearance(context.Background(), args.Appearance), nil
	})); err != nil {
		return nil, err
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "set_quirks",
		Description: "写入2～5条癖好。须在 set_appearance 之后。",
	}, func(_ adkagent.Context, args quirksArgs) (ToolResult, error) {
		return w.SetQuirks(context.Background(), args.Quirks), nil
	})); err != nil {
		return nil, err
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "write_soul",
		Description: "写入第一人称 SOUL 正文（约80～400字）。须在 set_quirks 之后。",
	}, func(_ adkagent.Context, args soulArgs) (ToolResult, error) {
		return w.WriteSoul(context.Background(), args.Narrative), nil
	})); err != nil {
		return nil, err
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "set_base_stats",
		Description: "设定初始数值（不含Health/EXP）。use_suggested=true 接受系统建议；否则在安全带内提交。须在 write_soul 之后。",
	}, func(_ adkagent.Context, args statsArgs) (ToolResult, error) {
		if args.UseSuggested || (args.Hunger == nil && args.Happy == nil && args.Clean == nil && args.Energy == nil) {
			return w.SetBaseStats(context.Background(), nil), nil
		}
		in := pet.Stats{}
		if args.Hunger != nil {
			in.Hunger = *args.Hunger
		}
		if args.Happy != nil {
			in.Happy = *args.Happy
		}
		if args.Clean != nil {
			in.Clean = *args.Clean
		}
		if args.Energy != nil {
			in.Energy = *args.Energy
		}
		return w.SetBaseStats(context.Background(), &in), nil
	})); err != nil {
		return nil, err
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "write_identity",
		Description: "写入名字、主人称呼与物种风味。须在 set_base_stats 之后。",
	}, func(_ adkagent.Context, args identityArgs) (ToolResult, error) {
		return w.WriteIdentity(context.Background(), args.Name, args.Master, args.SpeciesFlavor), nil
	})); err != nil {
		return nil, err
	}

	if err := add(functiontool.New(functiontool.Config{
		Name:        "finalize_birth",
		Description: "校验全部阶段并破壳出生。必须在最后调用。",
	}, func(_ adkagent.Context, _ noArgs) (ToolResult, error) {
		return w.FinalizeBirth(context.Background()), nil
	})); err != nil {
		return nil, err
	}

	return tools, nil
}
