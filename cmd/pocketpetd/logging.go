package main

import (
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-isatty"
)

// setupLogging 按配置初始化全局 logger：
// 终端（TTY）下用 tint 彩色输出（级别着色，便于肉眼扫日志）；
// 输出被重定向到文件/管道时退化为纯文本，避免 ANSI 转义序列污染日志文件。
// 设置 NO_COLOR 环境变量可强制关闭颜色（https://no-color.org 约定）。
func setupLogging(level string) {
	lv := parseLevel(level)
	w := os.Stderr
	color := (isatty.IsTerminal(w.Fd()) || isatty.IsCygwinTerminal(w.Fd())) && os.Getenv("NO_COLOR") == ""
	if color {
		slog.SetDefault(slog.New(tint.NewHandler(w, &tint.Options{
			Level:      lv,
			TimeFormat: time.DateTime,
		})))
		return
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: lv})))
}

// parseLevel 解析日志级别名，无法识别时回退 info。
func parseLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
