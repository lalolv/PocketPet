package pet

import "testing"

func TestMemorableEvent(t *testing.T) {
	cases := []struct {
		typ  string
		want bool
	}{
		// 经历类：保留
		{EventBorn, true},
		{EventStageUp, true},
		{EventFellAsleep, true},
		{EventWokeUp, true},
		{EventSkillLearned, true},
		{EventSoulChanged, true},
		{EventDead, true},
		{"pet.adventure_finished", true}, // 插件事件默认保留
		{"pet.friends_gift", true},
		// 身体预警：过滤（状态而非经历）
		{EventHungry, false},
		{EventDirty, false},
		{EventSleepy, false},
		{EventSick, false},
		{EventSad, false},
		// 宠物自述：过滤（已落日记/会话）
		{EventProactive, false},
		{EventDream, false},
		{EventDiaryWritten, false},
		// 诞生流程系统载荷：过滤
		{EventGenesisStarted, false},
		{EventGenesisNarration, false},
		{EventGenesisReady, false},
	}
	for _, c := range cases {
		if got := MemorableEvent(c.typ); got != c.want {
			t.Errorf("MemorableEvent(%q) = %v, want %v", c.typ, got, c.want)
		}
	}
}
