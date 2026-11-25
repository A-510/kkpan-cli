package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	// EnvPromptStyle 控制 REPL 提示符风格（starship / boxed）
	EnvPromptStyle = "KKPAN_PROMPT_STYLE"
	// MaxPromptDirLen 提示符目录截断宽度
	MaxPromptDirLen = 26
)

// PromptStyle 定义交互式 REPL 提示符风格
type PromptStyle string

const (
	PromptStyleStarship PromptStyle = "starship"
	PromptStyleBoxed    PromptStyle = "boxed"
	DefaultPromptStyle  PromptStyle = PromptStyleStarship
)

// ParsePromptStyle 校验并解析提示符风格
func ParsePromptStyle(raw string) (PromptStyle, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(PromptStyleStarship), "minimal":
		return PromptStyleStarship, nil
	case string(PromptStyleBoxed), "box":
		return PromptStyleBoxed, nil
	default:
		return "", fmt.Errorf("无效的提示符风格 %q (可选: starship, boxed)", raw)
	}
}

// EffectivePromptStyle 按「命令行 > 环境变量 > 配置文件 > 默认值」解析提示符风格
func EffectivePromptStyle(configured string) PromptStyle {
	if ov := GetPromptStyleOverride(); ov != "" {
		if p, err := ParsePromptStyle(ov); err == nil {
			return p
		}
	}
	if env := strings.TrimSpace(os.Getenv(EnvPromptStyle)); env != "" {
		if p, err := ParsePromptStyle(env); err == nil {
			return p
		}
	}
	if p, err := ParsePromptStyle(configured); err == nil {
		return p
	}
	return DefaultPromptStyle
}

// MaskUID 对账号 UID 中间部分脱敏，避免截屏泄漏完整身份标识
func MaskUID(uid string) string {
	s := strings.TrimSpace(uid)
	if s == "" {
		return "0"
	}
	runes := []rune(s)
	n := len(runes)
	switch {
	case n >= 8:
		return string(runes[:3]) + "****" + string(runes[n-3:])
	case n >= 5:
		return string(runes[:2]) + "***" + string(runes[n-2:])
	case n >= 2:
		return string(runes[:1]) + "**"
	default:
		return "***"
	}
}

// TruncateMiddleASCII 对过长网盘路径执行中间截断，优先保留根和末级目录名
func TruncateMiddleASCII(s string, maxWidth int, asciiOn bool) string {
	if maxWidth <= 0 || lipgloss.Width(s) <= maxWidth {
		return s
	}
	ell := "…"
	if asciiOn {
		ell = "..."
	}
	ellW := lipgloss.Width(ell)
	if maxWidth <= ellW+2 {
		return ansi.Truncate(s, maxWidth, ell)
	}
	runes := []rune(s)
	keep := (maxWidth - ellW) / 2
	if keep <= 0 {
		keep = 1
	}
	if 2*keep >= len(runes) {
		return s
	}
	return string(runes[:keep]) + ell + string(runes[len(runes)-keep:])
}

// BannerInfo 描述启动欢迎横幅渲染所需的会话状态
type BannerInfo struct {
	AppName    string
	Version    string
	Platform   string
	LoggedIn   bool
	Nickname   string
	UID        string
	MemberType string
	Workdir    string
	QuotaUsed  int64
	QuotaTotal int64
	HasQuota   bool
	Theme      ThemeName
	TermWidth  int
	Color      *bool
	ASCII      *bool
}

// RenderBanner 根据终端宽度自适应渲染日式二次元星轨启动欢迎面板
func RenderBanner(w io.Writer, info BannerInfo) string {
	return renderBanner(w, info, "")
}

func renderBanner(w io.Writer, info BannerInfo, mascot string) string {
	colorOn := ColorEnabled(w)
	if info.Color != nil {
		colorOn = *info.Color
	}
	asciiOn := ASCIIOnly()
	if info.ASCII != nil {
		asciiOn = *info.ASCII
	}
	width := info.TermWidth
	if width <= 0 {
		width = TerminalWidth()
	}
	if info.AppName == "" {
		info.AppName = AppName
	}
	if info.Workdir == "" {
		info.Workdir = "/"
	}

	p := ThemeByName(info.Theme)
	if info.Theme == "" {
		p = ActivePalette()
	}
	r := NewRenderer(w, &colorOn)

	border := lipgloss.RoundedBorder()
	star := "✦"
	sep := " · "
	if asciiOn {
		border = lipgloss.ASCIIBorder()
		star = "*"
		sep = " | "
	}

	accent := r.NewStyle().Foreground(p.Accent).Bold(true)
	sec := r.NewStyle().Foreground(p.Secondary).Bold(true)
	muted := r.NewStyle().Foreground(p.Muted)
	ink := r.NewStyle().Foreground(p.Ink).Bold(true)
	okStyle := r.NewStyle().Foreground(p.Success).Bold(true)
	warnStyle := r.NewStyle().Foreground(p.Warn).Bold(true)

	infoGap := "  "
	helpLine := "指令: 输入 'ui' 进入全屏二次元档案浏览器" + sep + "'help' 查看全部命令" + sep + "'exit' 退出"
	if mascot != "" {
		infoGap = "\n"
		helpLine = "ui 全屏浏览" + sep + "help 帮助" + sep + "exit 退出"
	}
	titleLine := accent.Render(star+" KK-PAN // 星海档案") + " " +
		RenderBadge(w, BadgeInfo, p.DisplayName, colorOn) + infoGap +
		muted.Render(info.Version+"  ("+info.Platform+")")

	var statusLine string
	if info.LoggedIn && info.Nickname != "" {
		dot := okStyle.Render("●")
		if asciiOn {
			dot = "*"
		}
		vipTag := ""
		if info.MemberType != "" {
			kind := BadgeNeutral
			if strings.Contains(strings.ToUpper(info.MemberType), "VIP") {
				kind = BadgeInfo
			}
			vipTag = " " + RenderBadge(w, kind, info.MemberType, colorOn)
		}
		statusLine = fmt.Sprintf("%s %s%s%s%s %s",
			dot, ink.Render(sanitizeSingleLine(info.Nickname)), vipTag, infoGap,
			muted.Render("路径:"), sec.Render(TruncateMiddleASCII(sanitizeSingleLine(info.Workdir), 32, asciiOn)),
		)
	} else {
		dot := warnStyle.Render("○")
		if asciiOn {
			dot = "o"
		}
		loginHint := "执行 'login -qr' 扫码登录或 'login -cookies' 导入"
		if mascot != "" {
			loginHint = "login -qr 扫码   login -cookies 导入"
		}
		statusLine = fmt.Sprintf("%s %s%s%s",
			dot, warnStyle.Render("未登录夸克账号"), infoGap,
			muted.Render(loginHint),
		)
	}

	var lines []string
	lines = append(lines, titleLine, statusLine)
	if info.HasQuota && info.QuotaTotal > 0 {
		ratio := float64(info.QuotaUsed) / float64(info.QuotaTotal)
		bar := RenderQuotaBar(w, ratio, 16, colorOn, asciiOn)
		lines = append(lines, fmt.Sprintf("%s %s  %s",
			muted.Render("空间:"),
			bar,
			muted.Render(fmt.Sprintf("%.1f%% (%s / %s)", ratio*100, FormatBytes(info.QuotaUsed), FormatBytes(info.QuotaTotal))),
		))
	}
	lines = append(lines, muted.Render(helpLine))

	content := strings.Join(lines, "\n")
	if width < 70 {
		return content + "\n"
	}

	boxW := width - 4
	if boxW > 106 {
		boxW = 106
	}
	if mascot != "" {
		const gap = "   "
		textWidth := boxW - 2 - mascotWidth - len(gap)
		content = lipgloss.JoinHorizontal(lipgloss.Center, mascot, gap, ansi.Wrap(content, textWidth, ""))
	}
	box := r.NewStyle().
		Border(border).
		BorderForeground(p.Hairline).
		Padding(0, 1).
		Width(boxW).
		Render(content)
	return box + "\n"
}

// PromptContext 描述交互式 REPL 提示符所需的实时状态
type PromptContext struct {
	Style      PromptStyle
	Theme      ThemeName
	LoggedIn   bool
	Nickname   string
	MemberType string
	Workdir    string
	QuotaUsed  int64
	QuotaTotal int64
	HasQuota   bool
	TermWidth  int
	Color      *bool
	ASCII      *bool
}

// FormatPrompt 生成双行式 REPL 提示符：headerLine 展示彩色星轨状态，
// inputPrompt 严格仅含纯可打印 ASCII/符号（绝无 ANSI 控制序列），保证与 peterh/liner 光标对齐。
func FormatPrompt(w io.Writer, ctx PromptContext) (headerLine string, inputPrompt string) {
	colorOn := ColorEnabled(w)
	if ctx.Color != nil {
		colorOn = *ctx.Color
	}
	asciiOn := ASCIIOnly()
	if ctx.ASCII != nil {
		asciiOn = *ctx.ASCII
	}
	width := ctx.TermWidth
	if width <= 0 {
		width = TerminalWidth()
	}

	p := ThemeByName(ctx.Theme)
	if ctx.Theme == "" {
		p = ActivePalette()
	}
	r := NewRenderer(w, &colorOn)

	style := EffectivePromptStyle("")
	if ctx.Style != "" {
		style, _ = ParsePromptStyle(string(ctx.Style))
	}

	nick := sanitizeSingleLine(ctx.Nickname)
	wd := sanitizeSingleLine(ctx.Workdir)
	if wd == "" {
		wd = "/"
	}

	accent := r.NewStyle().Foreground(p.Accent).Bold(true)
	sec := r.NewStyle().Foreground(p.Secondary).Bold(true)
	muted := r.NewStyle().Foreground(p.Muted)
	ink := r.NewStyle().Foreground(p.Ink).Bold(true)
	warn := r.NewStyle().Foreground(p.Warn)

	star := "✦"
	sep := " · "
	arrow := "> "
	boxedCornerTop := "╭─ "
	boxedCornerBot := "╰─> "
	if asciiOn {
		star = "*"
		sep = " | "
		boxedCornerTop = "+- "
		boxedCornerBot = "`-> "
	}

	var left string
	if !ctx.LoggedIn || nick == "" {
		left = accent.Render(star+" kkpan") + "  " + warn.Render("未登录") + " " + muted.Render("(login -qr)")
	} else {
		left = accent.Render(star + " " + nick)
		if ctx.MemberType != "" {
			left += " " + muted.Render("["+sanitizeSingleLine(ctx.MemberType)+"]")
		}
		left += "  " + sec.Render(TruncateMiddleASCII(wd, MaxPromptDirLen, asciiOn))
	}

	right := ""
	if ctx.HasQuota && ctx.QuotaTotal > 0 {
		ratio := float64(ctx.QuotaUsed) / float64(ctx.QuotaTotal) * 100
		right = muted.Render(fmt.Sprintf("%.1f%%%s%s/%s", ratio, sep, FormatBytes(ctx.QuotaUsed), FormatBytes(ctx.QuotaTotal)))
	}

	if style == PromptStyleBoxed {
		if right != "" {
			headerLine = r.NewStyle().Foreground(p.Hairline).Render(boxedCornerTop) + left + "  " + right
		} else {
			headerLine = r.NewStyle().Foreground(p.Hairline).Render(boxedCornerTop) + left
		}
		headerLine = ansi.Truncate(headerLine, max(10, width-1), "")
		return headerLine, boxedCornerBot
	}

	// starship 极简右对齐布局
	maxW := max(10, width-1)
	if right != "" {
		gap := maxW - lipgloss.Width(left) - lipgloss.Width(right)
		if gap >= 2 {
			headerLine = left + strings.Repeat(" ", gap) + right
		} else {
			headerLine = ansi.Truncate(left, maxW, "")
		}
	} else {
		headerLine = ansi.Truncate(left, maxW, "")
	}
	_ = ink
	return headerLine, arrow
}

func sanitizeSingleLine(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' || unicode.IsControl(r) {
			continue
		}
		if utf8.ValidRune(r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}
