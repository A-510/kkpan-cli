package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnimeThemeSwitching(t *testing.T) {
	if CycleTheme(ThemeCelestial) != ThemeDossier {
		t.Fatalf("celestial 下一主题应为 dossier")
	}
	if CycleTheme(ThemeDossier) != ThemeShinbo {
		t.Fatalf("dossier 下一主题应为 shinbo")
	}
	if CycleTheme(ThemeShinbo) != ThemeCelestial {
		t.Fatalf("shinbo 下一主题应回环为 celestial")
	}

	SetThemeOverride("shinbo")
	defer SetThemeOverride("")
	p := ActivePalette()
	if p.Name != ThemeShinbo || p.DisplayName != "物语赤黑" {
		t.Fatalf("主题覆盖未生效: %+v", p)
	}
}

func TestTableRenderUnicodeAndASCII(t *testing.T) {
	var buf bytes.Buffer
	colorOff := false
	asciiOff := false
	out := RenderTable(&buf, TableConfig{
		Headers: []string{"#", "文件名", "大小"},
		Rows: [][]string{
			{"01", "紫罗兰永恒花园_BD.mkv", "1.42 GB"},
			{"02", "空之境界_原声带.flac", "412.0 MB"},
		},
		Alignments: []TextAlign{AlignRight, AlignLeft, AlignRight},
		MaxWidth:   80,
		Color:      &colorOff,
		ASCII:      &asciiOff,
	})
	if !strings.Contains(out, "╭") || !strings.Contains(out, "紫罗兰永恒花园") {
		t.Fatalf("Unicode 圆角表格输出异常:\n%s", out)
	}

	asciiOn := true
	outASCII := RenderTable(&buf, TableConfig{
		Headers:  []string{"#", "文件名", "大小"},
		Rows:     [][]string{{"01", "超长测试中文文件名称用于校验七位纯ASCII省略号截断是否安全", "10.0 MB"}},
		MaxWidth: 42,
		Color:    &colorOff,
		ASCII:    &asciiOn,
	})
	if strings.Contains(outASCII, "╭") || strings.Contains(outASCII, "…") {
		t.Fatalf("ASCII 降级模式下不应出现 Unicode 框线或省略号:\n%s", outASCII)
	}
}

func TestQRRenderAndPNG(t *testing.T) {
	var buf bytes.Buffer
	rendered, w, err := RenderQRMatrixTerminal(&buf, "https://su.quark.cn/4_eMHBJ?token=demo", false, false)
	if err != nil || w <= 0 || !strings.ContainsAny(rendered, "█▀▄") {
		t.Fatalf("二维码终端渲染失败: w=%d err=%v", w, err)
	}

	pngPath := filepath.Join(t.TempDir(), "qr.png")
	if err := WriteQRPNGFile("https://su.quark.cn/4_eMHBJ?token=demo", pngPath); err != nil {
		t.Fatalf("WriteQRPNGFile 失败: %v", err)
	}
}

func TestPromptHasNoANSIInInputAndJSONTSV(t *testing.T) {
	colorOn := true
	asciiOff := false
	for _, style := range []PromptStyle{PromptStyleStarship, PromptStyleBoxed} {
		hdr, input := FormatPrompt(&bytes.Buffer{}, PromptContext{
			Style:      style,
			Theme:      ThemeCelestial,
			LoggedIn:   true,
			Nickname:   "星野",
			MemberType: "SVIP",
			Workdir:    "/番剧/新番",
			QuotaUsed:  500,
			QuotaTotal: 1000,
			HasQuota:   true,
			TermWidth:  100,
			Color:      &colorOn,
			ASCII:      &asciiOff,
		})
		if hdr == "" || strings.Contains(input, "\x1b") {
			t.Fatalf("提示符异常 (style=%s): hdr=%q input=%q", style, hdr, input)
		}
	}

	var jsonBuf bytes.Buffer
	if err := WriteJSON(&jsonBuf, map[string]string{"name": "测试"}); err != nil {
		t.Fatal(err)
	}
	var parsed map[string]string
	if err := json.Unmarshal(jsonBuf.Bytes(), &parsed); err != nil || parsed["name"] != "测试" {
		t.Fatalf("WriteJSON 输出不合法: %s", jsonBuf.String())
	}

	var tsvBuf bytes.Buffer
	if err := WriteTSV(&tsvBuf, [][]string{{"a\tb", "c\nd"}}); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(tsvBuf.String()) != "a b\tc d" {
		t.Fatalf("WriteTSV 清理制表符/换行异常: %q", tsvBuf.String())
	}
}

func TestFriendlyErrorHintQuarkEOF(t *testing.T) {
	err := errors.New(`探测下载链接失败: Get "https://dl-pc-sz.drive.quark.cn/abc/demo.pdf": EOF`)
	hint := FriendlyErrorHint(err)
	if !strings.Contains(hint, "Direct") || !strings.Contains(hint, "*.quark.cn") {
		t.Fatalf("期望返回代理直连排错提示，实际: %q", hint)
	}
}
