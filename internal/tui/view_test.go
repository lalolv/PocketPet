package tui

import (
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

// TestWrapText 验证长行按显示宽度软换行：CJK 双宽、硬换行保留、不超宽。
func TestWrapText(t *testing.T) {
	// 短行不换行
	if got := wrapText("你好", 10); len(got) != 1 || got[0] != "你好" {
		t.Fatalf("short line = %v", got)
	}
	// CJK 按双宽计：10 个汉字 = 20 宽，宽度 10 → 两行各 5 字
	got := wrapText("一二三四五六七八九十", 10)
	if len(got) != 2 || got[0] != "一二三四五" || got[1] != "六七八九十" {
		t.Fatalf("cjk wrap = %v", got)
	}
	for _, l := range got {
		if runewidth.StringWidth(l) > 10 {
			t.Fatalf("line %q exceeds width", l)
		}
	}
	// 混合中英 + 硬换行保留
	got = wrapText("ab你 cd\nef", 4)
	if len(got) != 3 || got[0] != "ab你" || got[1] != " cd" || got[2] != "ef" {
		t.Fatalf("mixed wrap = %v", got)
	}
	// width <= 0：不换行，仅按硬换行拆分
	if got := wrapText("abc\ndef", 0); len(got) != 2 || strings.Join(got, "\n") != "abc\ndef" {
		t.Fatalf("no wrap = %v", got)
	}
}
