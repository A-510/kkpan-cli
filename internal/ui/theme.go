package ui

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/mattn/go-runewidth"
	"github.com/muesli/termenv"
	"golang.org/x/term"
)

func init() {
	// 现代 UTF-8 终端将框线/星芒/圆点渲染为 1 列，锁定 EastAsianWidth=false 防止 liner 光标计算右偏
	if os.Getenv("RUNEWIDTH_EASTASIAN") != "1" {
		runewidth.EastAsianWidth = false
		runewidth.DefaultCondition.EastAsianWidth = false
	}
}

const (
	// BrandName 项目展示名称
	BrandName = "kkpan-cli"
	// AppName 默认 CLI 命令名
	AppName = "kkpan"
	// BrandTag 副标识
	BrandTag = "星海夸克网盘终端"
	// EnvASCII 强制纯 ASCII 环境变量
	EnvASCII = "KKPAN_ASCII"
	// EnvTheme 预设日式二次元主题环境变量
	EnvTheme = "KKPAN_THEME"
)

// ThemeName 表示日式二次元视觉主题名
type ThemeName string

const (
	// ThemeCelestial 星穹星海（默认，源自 design-pipeline celestial-voyage）
	ThemeCelestial ThemeName = "celestial"
	// ThemeDossier 终末档案（源自 design-pipeline lattice-dossier / arknights-endfield）
	ThemeDossier ThemeName = "dossier"
	// ThemeShinbo 物语赤黑（源自 design-pipeline shinbo）
	ThemeShinbo ThemeName = "shinbo"
)

// ThemePalette 定义日式二次元终端语义色板（严格遵循 2-3 个有彩色 + 1 组中性色收敛协议）
type ThemePalette struct {
	Name        ThemeName
	DisplayName string
	BadgeTag    string
	Accent      lipgloss.Color // 主强调色（星轨金 / 信号青 / 朱绯红）
	Secondary   lipgloss.Color // 次强调色（天青 / 战术琥珀 / 琥珀金）
	Highlight   lipgloss.Color // 点睛色（樱绯 / 翡翠 / 纸白，控制在 <=5% 面积）
	Primary     lipgloss.Color // 标题与结构主色
	Success     lipgloss.Color
	Warn        lipgloss.Color
	Fail        lipgloss.Color
	Ink         lipgloss.Color // 主正文墨白
	Muted       lipgloss.Color // 编号轨与次要注释
	Hairline    lipgloss.Color // 框线与分割发丝线
	RowSelectBg lipgloss.Color // 当前选中行柔和高亮底色
	BadgeInk    lipgloss.Color // 实底徽章反白字色
}

var themes = map[ThemeName]ThemePalette{
	ThemeCelestial: {
		Name:        ThemeCelestial,
		DisplayName: "星穹星海",
		BadgeTag:    "星穹",
		Accent:      lipgloss.Color("#E1B066"), // 鎏金星轨
		Secondary:   lipgloss.Color("#7DCFFF"), // 星海天青
		Highlight:   lipgloss.Color("#F29CB8"), // 樱绯点睛
		Primary:     lipgloss.Color("#E1B066"),
		Success:     lipgloss.Color("#7EE7B8"),
		Warn:        lipgloss.Color("#F5C26B"),
		Fail:        lipgloss.Color("#F38BA8"),
		Ink:         lipgloss.Color("#EAEFF8"),
		Muted:       lipgloss.Color("#8FA3C8"),
		Hairline:    lipgloss.Color("#364B78"),
		RowSelectBg: lipgloss.Color("#1C2C4E"),
		BadgeInk:    lipgloss.Color("#0B1426"),
	},
	ThemeDossier: {
		Name:        ThemeDossier,
		DisplayName: "终末档案",
		BadgeTag:    "档案",
		Accent:      lipgloss.Color("#38BDF8"), // 冰川信号青
		Secondary:   lipgloss.Color("#93C5FD"), // 浅钢蓝
		Highlight:   lipgloss.Color("#F59E0B"), // 战术琥珀
		Primary:     lipgloss.Color("#38BDF8"),
		Success:     lipgloss.Color("#34D399"),
		Warn:        lipgloss.Color("#F59E0B"),
		Fail:        lipgloss.Color("#F87171"),
		Ink:         lipgloss.Color("#E6EDF3"),
		Muted:       lipgloss.Color("#7D8590"),
		Hairline:    lipgloss.Color("#303A4D"),
		RowSelectBg: lipgloss.Color("#1A2B3C"),
		BadgeInk:    lipgloss.Color("#071019"),
	},
	ThemeShinbo: {
		Name:        ThemeShinbo,
		DisplayName: "物语赤黑",
		BadgeTag:    "物语",
		Accent:      lipgloss.Color("#E63946"), // 朱绯红
		Secondary:   lipgloss.Color("#E9B44C"), // 琥珀金
		Highlight:   lipgloss.Color("#F4F1EA"), // 纸白
		Primary:     lipgloss.Color("#E63946"),
		Success:     lipgloss.Color("#8AC926"),
		Warn:        lipgloss.Color("#E9B44C"),
		Fail:        lipgloss.Color("#FF595E"),
		Ink:         lipgloss.Color("#F4F1EA"),
		Muted:       lipgloss.Color("#9A95A1"),
		Hairline:    lipgloss.Color("#45414F"),
		RowSelectBg: lipgloss.Color("#2D1419"),
		BadgeInk:    lipgloss.Color("#14090C"),
	},
}

var themeOrder = []ThemeName{ThemeCelestial, ThemeDossier, ThemeShinbo}

var (
	noColorOverride     atomic.Bool
	asciiOverride       atomic.Bool
	quietOverride       atomic.Bool
	promptStyleOverride atomic.Value
	themeOverride       atomic.Value
)

// ParseThemeName 校验并规范化主题名称
func ParseThemeName(raw string) (ThemeName, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ThemeCelestial):
		return ThemeCelestial, nil
	case string(ThemeDossier), "endfield":
		return ThemeDossier, nil
	case string(ThemeShinbo), "monogatari":
		return ThemeShinbo, nil
	default:
		return "", fmt.Errorf("未知的主题 %q (可选: celestial, dossier, shinbo)", raw)
	}
}

// CycleTheme 返回按序切换的下一套日式二次元主题
func CycleTheme(current ThemeName) ThemeName {
	for i, name := range themeOrder {
		if name == current {
			return themeOrder[(i+1)%len(themeOrder)]
		}
	}
	return ThemeCelestial
}

// ThemeByName 根据名称获取色板，未知时回退到默认的星穹星海主题
func ThemeByName(name ThemeName) ThemePalette {
	if p, ok := themes[name]; ok {
		return p
	}
	return themes[ThemeCelestial]
}

// SetThemeOverride 设置命令行级别的主题覆盖（空串清除覆盖）
func SetThemeOverride(name string) {
	if strings.TrimSpace(name) == "" {
		themeOverride.Store("")
		return
	}
	if parsed, err := ParseThemeName(name); err == nil {
		themeOverride.Store(string(parsed))
	}
}

// GetThemeOverride 获取当前主题覆盖值
func GetThemeOverride() string {
	v, _ := themeOverride.Load().(string)
	return v
}

// EffectiveTheme 解析当前生效的主题（优先级：CLI 覆盖 > KKPAN_THEME > 配置文件 > 默认 celestial）
func EffectiveTheme(configured string) ThemePalette {
	if ov := GetThemeOverride(); ov != "" {
		if parsed, err := ParseThemeName(ov); err == nil {
			return ThemeByName(parsed)
		}
	}
	if env := strings.TrimSpace(os.Getenv(EnvTheme)); env != "" {
		if parsed, err := ParseThemeName(env); err == nil {
			return ThemeByName(parsed)
		}
	}
	if parsed, err := ParseThemeName(configured); err == nil {
		return ThemeByName(parsed)
	}
	return themes[ThemeCelestial]
}

// ActivePalette 返回当前全局激活的主题色板
func ActivePalette() ThemePalette {
	return EffectiveTheme("")
}

// UIOverrides 保存全局 UI 状态快照，供 REPL 单条命令后恢复基线
type UIOverrides struct {
	NoColor     bool
	ASCII       bool
	Quiet       bool
	PromptStyle string
	Theme       string
}

// SnapshotOverrides 获取当前 UI 开关快照
func SnapshotOverrides() UIOverrides {
	return UIOverrides{
		NoColor:     noColorOverride.Load(),
		ASCII:       asciiOverride.Load(),
		Quiet:       quietOverride.Load(),
		PromptStyle: GetPromptStyleOverride(),
		Theme:       GetThemeOverride(),
	}
}

// RestoreOverrides 恢复 UI 开关快照
func RestoreOverrides(snap UIOverrides) {
	SetNoColor(snap.NoColor)
	SetASCIIOnly(snap.ASCII)
	SetQuiet(snap.Quiet)
	SetPromptStyleOverride(snap.PromptStyle)
	SetThemeOverride(snap.Theme)
}

// SetPromptStyleOverride 设置提示符风格覆盖值
func SetPromptStyleOverride(style string) {
	if style == "" {
		promptStyleOverride.Store("")
		return
	}
	if parsed, err := ParsePromptStyle(style); err == nil {
		promptStyleOverride.Store(string(parsed))
		return
	}
	promptStyleOverride.Store("")
}

// GetPromptStyleOverride 获取提示符风格覆盖值
func GetPromptStyleOverride() string {
	v, _ := promptStyleOverride.Load().(string)
	return v
}

// SetNoColor 全局启用或禁用 ANSI 彩色输出
func SetNoColor(disable bool) {
	noColorOverride.Store(disable)
	if disable {
		lipgloss.SetColorProfile(termenv.Ascii)
	} else {
		lipgloss.SetColorProfile(termenv.EnvColorProfile())
	}
}

// SetASCIIOnly 全局强制纯 7-bit ASCII 降级
func SetASCIIOnly(ascii bool) {
	asciiOverride.Store(ascii)
}

// SetQuiet 设置静默模式
func SetQuiet(quiet bool) {
	quietOverride.Store(quiet)
}

// IsQuiet 返回是否处于静默模式
func IsQuiet() bool {
	return quietOverride.Load()
}

// IsTTY 判断目标 Writer 是否直接连接交互式终端
func IsTTY(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	fd := f.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// ColorEnabled 判断是否应当输出 ANSI 颜色序列
func ColorEnabled(w io.Writer) bool {
	if noColorOverride.Load() || os.Getenv("NO_COLOR") != "" || os.Getenv("CLICOLOR") == "0" {
		return false
	}
	if strings.EqualFold(os.Getenv("TERM"), "dumb") {
		return false
	}
	if force := os.Getenv("FORCE_COLOR"); force != "" && force != "0" {
		return true
	}
	if force := os.Getenv("CLICOLOR_FORCE"); force != "" && force != "0" {
		return true
	}
	if _, ok := w.(*os.File); ok {
		return IsTTY(w)
	}
	return IsTTY(os.Stdout)
}

// ASCIIOnly 判断是否应当降级为纯 ASCII 符号
func ASCIIOnly() bool {
	if asciiOverride.Load() || os.Getenv(EnvASCII) == "1" || os.Getenv("RUNEWIDTH_EASTASIAN") == "1" {
		return true
	}
	for _, key := range []string{"LC_ALL", "LANG"} {
		v := strings.ToUpper(os.Getenv(key))
		if v == "C" || v == "POSIX" {
			return true
		}
	}
	return false
}

// TerminalWidth 返回当前终端可用列宽（失败回退到 COLUMNS 或 80）
func TerminalWidth() int {
	for _, f := range []*os.File{os.Stdout, os.Stderr} {
		if f != nil && IsTTY(f) {
			if w, _, err := term.GetSize(int(f.Fd())); err == nil && w > 0 {
				return clampWidth(w)
			}
		}
	}
	if cols := os.Getenv("COLUMNS"); cols != "" {
		if n, err := strconv.Atoi(cols); err == nil && n > 0 {
			return clampWidth(n)
		}
	}
	return 80
}

func clampWidth(w int) int {
	if w < 40 {
		return 40
	}
	if w > 160 {
		return 160
	}
	return w
}

// NewRenderer 创建绑定目标 Writer 与显式色彩开关的 Lipgloss 渲染器
func NewRenderer(w io.Writer, colorOverride *bool) *lipgloss.Renderer {
	if w == nil {
		w = os.Stdout
	}
	r := lipgloss.NewRenderer(w)
	enabled := ColorEnabled(w)
	if colorOverride != nil {
		enabled = *colorOverride
	}
	if !enabled {
		r.SetColorProfile(termenv.Ascii)
	} else if r.ColorProfile() == termenv.Ascii {
		r.SetColorProfile(termenv.TrueColor)
	}
	return r
}
