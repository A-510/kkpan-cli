package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// FormatBytes 将字节数格式化为人类可读体积字符串（1024 进制）
func FormatBytes(size int64) string {
	if size < 0 {
		return "-"
	}
	const (
		kb = 1024
		mb = 1024 * kb
		gb = 1024 * mb
		tb = 1024 * gb
	)
	switch {
	case size >= tb:
		return fmt.Sprintf("%.2f TB", float64(size)/tb)
	case size >= gb:
		return fmt.Sprintf("%.2f GB", float64(size)/gb)
	case size >= mb:
		return fmt.Sprintf("%.2f MB", float64(size)/mb)
	case size >= kb:
		return fmt.Sprintf("%.1f KB", float64(size)/kb)
	default:
		return fmt.Sprintf("%d B", size)
	}
}

// BadgeKind 标识胶囊徽章语义样式
type BadgeKind int

const (
	BadgeNeutral BadgeKind = iota
	BadgeInfo
	BadgeSuccess
	BadgeWarn
	BadgeFail
)

// RenderBadge 渲染紧凑日式胶囊徽章
func RenderBadge(w io.Writer, kind BadgeKind, text string, colorOn bool) string {
	p := ActivePalette()
	r := NewRenderer(w, &colorOn)
	bg := p.Hairline
	fg := p.Ink
	switch kind {
	case BadgeInfo:
		bg = p.Accent
		fg = p.BadgeInk
	case BadgeSuccess:
		bg = p.Success
		fg = p.BadgeInk
	case BadgeWarn:
		bg = p.Warn
		fg = p.BadgeInk
	case BadgeFail:
		bg = p.Fail
		fg = p.BadgeInk
	}
	return r.NewStyle().
		Background(bg).
		Foreground(fg).
		Bold(true).
		Padding(0, 1).
		Render(text)
}

// RenderQuotaBar 渲染星轨风格容量进度条（如 ━━━◈─────）
func RenderQuotaBar(w io.Writer, ratio float64, width int, colorOn, ascii bool) string {
	if width <= 0 {
		width = 10
	}
	if ratio < 0 {
		ratio = 0
	}
	if ratio > 1 {
		ratio = 1
	}
	filled := int(ratio * float64(width))
	if filled > width {
		filled = width
	}

	fillCh := "━"
	headCh := "◈"
	emptyCh := "─"
	if ascii {
		fillCh = "="
		headCh = ">"
		emptyCh = "-"
	}

	p := ActivePalette()
	r := NewRenderer(w, &colorOn)

	fillColor := p.Accent
	if ratio >= 0.90 {
		fillColor = p.Fail
	} else if ratio >= 0.75 {
		fillColor = p.Warn
	}

	var fillPart, headPart, emptyPart string
	if filled == 0 {
		headPart = ""
		emptyPart = strings.Repeat(emptyCh, width)
	} else if filled >= width {
		fillPart = strings.Repeat(fillCh, width-1)
		headPart = headCh
		emptyPart = ""
	} else {
		fillPart = strings.Repeat(fillCh, filled-1)
		headPart = headCh
		emptyPart = strings.Repeat(emptyCh, width-filled)
	}

	return r.NewStyle().Foreground(fillColor).Bold(true).Render(fillPart+headPart) +
		r.NewStyle().Foreground(p.Hairline).Render(emptyPart)
}

// ProgressPrinter 提供非静默 TTY 下的单行传输进度刷新，在静默或管道下自动关闭
type ProgressPrinter struct {
	mu       sync.Mutex
	out      io.Writer
	enabled  bool
	ascii    bool
	colorOn  bool
	lastDraw time.Time
}

// NewProgressPrinter 创建单行传输进度条输出器
func NewProgressPrinter(out io.Writer) *ProgressPrinter {
	if out == nil {
		out = os.Stderr
	}
	enabled := !IsQuiet() && IsTTY(out)
	return &ProgressPrinter{
		out:     out,
		enabled: enabled,
		ascii:   ASCIIOnly(),
		colorOn: ColorEnabled(out),
	}
}

// Update 刷新进度显示（自动限频 120ms，完成时输出换行）
func (pp *ProgressPrinter) Update(name string, completed, total int64, bytesPerSec float64, done bool) {
	if pp == nil || !pp.enabled {
		return
	}
	pp.mu.Lock()
	defer pp.mu.Unlock()

	if !done && time.Since(pp.lastDraw) < 120*time.Millisecond {
		return
	}
	pp.lastDraw = time.Now()

	var ratio float64
	if total > 0 {
		ratio = float64(completed) / float64(total)
	}
	bar := RenderQuotaBar(pp.out, ratio, 18, pp.colorOn, pp.ascii)
	pct := ratio * 100
	if pct > 100 {
		pct = 100
	}

	etaStr := "--:--"
	if bytesPerSec > 0 && total > completed {
		remSec := int64(float64(total-completed) / bytesPerSec)
		etaStr = fmt.Sprintf("%02d:%02d", remSec/60, remSec%60)
	} else if done {
		etaStr = "00:00"
	}

	shortName := name
	if len([]rune(shortName)) > 24 {
		runes := []rune(shortName)
		shortName = string(runes[:21]) + "..."
	}

	line := fmt.Sprintf("\r  %s  %s %5.1f%%  %s/%s  %s/s  ETA %s",
		shortName,
		bar,
		pct,
		FormatBytes(completed),
		FormatBytes(total),
		FormatBytes(int64(bytesPerSec)),
		etaStr,
	)
	if done {
		fmt.Fprintln(pp.out, line)
	} else {
		fmt.Fprint(pp.out, line)
	}
}
