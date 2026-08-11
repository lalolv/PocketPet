package metaagent

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// RunScript 用确定性内容按阶段调用工具（无 LLM / ForceScript）。
// 每一步先发旁白，再调工具；阶段间可按 ScriptPace 停顿；失败则 FillMissing + finalize。
func (m *Midwife) RunScript(ctx context.Context, petID string) (string, error) {
	w, err := m.loadWorkshop(petID)
	if err != nil {
		return "", err
	}
	if err := w.setVia(ViaScript); err != nil {
		return "", err
	}
	d := w.draft

	pace := func() {
		if m.ScriptPace <= 0 {
			return
		}
		t := time.NewTimer(m.ScriptPace)
		defer t.Stop()
		select {
		case <-ctx.Done():
		case <-t.C:
		}
	}

	step := func(stage string, narrate string, call func() ToolResult) bool {
		if ctx.Err() != nil {
			return false
		}
		w.Narrate(ctx, stage, narrate)
		res := call()
		if !res.OK {
			slog.Warn("metaagent: script step failed", "pet", petID, "stage", stage, "err", res.Error)
			return false
		}
		pace()
		return true
	}

	ok := step("genes", "蛋壳上浮现出细小的光纹，像有什么在里面苏醒……", func() ToolResult {
		return w.RollGenes(ctx, nil)
	})
	if !ok {
		return m.finishWithFallback(ctx, w, "genes failed")
	}

	label, blurb := fallbackTemperament(d.Traits)
	ok = step("temperament", "光线收束成一种气质，你隐约感觉到它会是什么样的性格。", func() ToolResult {
		return w.SetTemperament(ctx, label, blurb)
	})
	if !ok {
		return m.finishWithFallback(ctx, w, "temperament failed")
	}

	ok = step("appearance", "剪影一点点清晰，毛色与轮廓从雾气里走出来。", func() ToolResult {
		return w.SetAppearance(ctx, fallbackAppearance(d.Species, d.Traits))
	})
	if !ok {
		return m.finishWithFallback(ctx, w, "appearance failed")
	}

	ok = step("quirks", "一些小小的怪癖跟着心跳一起成型。", func() ToolResult {
		return w.SetQuirks(ctx, fallbackQuirks(d.Traits))
	})
	if !ok {
		return m.finishWithFallback(ctx, w, "quirks failed")
	}

	ok = step("soul", "灵魂落进身体里，开始用第一人称认识这个世界。", func() ToolResult {
		return w.WriteSoul(ctx, fallbackSoul(d))
	})
	if !ok {
		return m.finishWithFallback(ctx, w, "soul failed")
	}

	ok = step("stats", "生命力在身体里铺开，呼吸变得平稳。", func() ToolResult {
		return w.SetBaseStats(ctx, nil)
	})
	if !ok {
		return m.finishWithFallback(ctx, w, "stats failed")
	}

	ok = step("identity", "它抬起头，好像在等一个名字。", func() ToolResult {
		return w.WriteIdentity(ctx, d.ReqName, d.Master, fallbackFlavor(d.Species))
	})
	if !ok {
		return m.finishWithFallback(ctx, w, "identity failed")
	}

	w.Narrate(ctx, "finalize", "蛋壳裂开一道缝，新的生命探出头来。")
	res := w.FinalizeBirth(ctx)
	if !res.OK {
		return m.finishWithFallback(ctx, w, res.Error)
	}
	slog.Info("metaagent: birth ready", "pet", petID, "name", d.Name, "via", ViaScript)
	return ViaScript, nil
}

func (m *Midwife) finishWithFallback(ctx context.Context, w *Workshop, reason string) (string, error) {
	m.emitFailed(ctx, w.draft.PetID, reason, true)
	res := w.EnsureComplete(ctx)
	if !res.OK {
		return ViaFallback, fmt.Errorf("metaagent: fallback finalize failed: %s", res.Error)
	}
	return ViaFallback, nil
}
