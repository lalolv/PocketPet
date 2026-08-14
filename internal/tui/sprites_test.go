package tui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// TestSpriteFramesUniform 帧数据规整性：所有帧 4-5 行高、无尾部空行、行宽 ≤14。
// 渲染层的固定盒子（view.go fitLines）以这两条约定为前提；
// 曾经 6 行的 Play[1] 帧把精灵区周期性撑高 1 行，导致整屏抖动。
func TestSpriteFramesUniform(t *testing.T) {
	check := func(species, action, frame string) {
		lines := strings.Split(strings.TrimPrefix(frame, "\n"), "\n")
		if n := len(lines); n < 4 || n > 5 {
			t.Errorf("%s/%s 帧高 %d 行，超出 4-5 行约定", species, action, n)
		}
		if strings.TrimSpace(lines[len(lines)-1]) == "" {
			t.Errorf("%s/%s 帧有尾部空行", species, action)
		}
		for _, l := range lines {
			if w := runewidth.StringWidth(l); w > 14 {
				t.Errorf("%s/%s 帧行宽 %d 超过 14：%q", species, action, w, l)
			}
		}
	}
	for species, sp := range sprites {
		sets := map[string][]string{
			"Idle": sp.Idle, "Sleep": sp.Sleep, "Eat": sp.Eat,
			"Play": sp.Play, "Clean": sp.Clean,
		}
		for action, frames := range sets {
			for _, f := range frames {
				check(species, action, f)
			}
		}
		check(species, "Dead", sp.Dead)
	}
}
