package agent

import (
	"fmt"

	"github.com/lalolv/PocketPet/internal/pet"
)

// deadLine 是宠物死亡后对话的固定回应。
const deadLine = "（一片安静，再也没有回应……）"

// fallbackLines 是 LLM 不可用时的预置话术，按 SOUL 模板区分。
// 每条含一个 %s，填入当前心情短语；同一只宠物按序号轮换。
var fallbackLines = map[string][]string{
	"lively": {
		"我在呢我在呢！就算%s，也想跟你玩！",
		"主人你来啦！%s也挡不住我开心！",
		"嘿嘿，%s，不过你一跟我说话我就来精神了！",
		"陪我玩嘛陪我玩嘛！%s也没关系！",
	},
	"quiet": {
		"嗯，我在。%s，但能这样陪着你，就挺好的。",
		"主人，你来了。%s……我们安静待一会儿吧。",
		"我在听。%s，不急，慢慢说。",
		"你来了就好。%s，没关系的。",
	},
	"tsundere": {
		"哼，就算%s，才、才不是想让你担心呢！",
		"干嘛啦……%s又怎样！说吧，我勉强听听。",
		"才不是特意等你的！%s，哼。",
		"%s……哎，看在你诚心诚意的份上，陪你聊两句好了。",
	},
}

// genericFallback 是未匹配到模板（如手写 SOUL.md）时的话术。
var genericFallback = []string{
	"我在呢。就算%s，你跟我说话我也很开心。",
	"主人，%s。你来了真好。",
	"%s……不过没关系，跟我说说话吧。",
}

// fallbackLine 为宠物挑一条降级话术：按 SOUL 模板选词库，按对话序号轮换，
// %s 填入由实时状态推出的心情短语。
func (a *PetAgent) fallbackLine(p *pet.Pet) string {
	template, err := a.fs.SoulTemplate(p.ID)
	if err != nil {
		template = ""
	}
	lines, ok := fallbackLines[template]
	if !ok {
		lines = genericFallback
	}

	a.mu.Lock()
	i := a.seq[p.ID]
	a.seq[p.ID] = i + 1
	a.mu.Unlock()

	return fmt.Sprintf(lines[i%len(lines)], moodPhrase(p))
}

// moodPhrase 从数值状态推出一句生命化的心情短语（降级文案与状态快照共用）。
// 按严重程度取最先命中的一项。
func moodPhrase(p *pet.Pet) string {
	switch {
	case p.Sleeping:
		return "正困得睁不开眼"
	case p.Stats.Health < pet.SickBelow:
		return "身子不太舒服"
	case p.Stats.Hunger < pet.AlertWarn:
		return "肚子饿得咕咕叫"
	case p.Stats.Energy < pet.AlertWarn:
		return "累得抬不起头"
	case p.Stats.Clean < pet.AlertWarn:
		return "身上脏兮兮的"
	case p.Stats.Happy < pet.AlertWarn:
		return "心里有点闷闷的"
	default:
		return "精神好得很"
	}
}
