package ui

import (
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/lipgloss/table"
)

// TextAlign 表示表格列对齐方式
type TextAlign int

const (
	AlignDefault TextAlign = iota
	AlignCenter
	AlignRight
	AlignLeft
)

// TableConfig 控制自适应宽度表格的渲染外观
type TableConfig struct {
	Headers     []string
	Rows        [][]string
	Alignments  []TextAlign
	HeaderAlign *TextAlign
	MaxWidth    int
	Color       *bool
	ASCII       *bool
}

// RenderTable 渲染圆角 Unicode 或纯 ASCII 表格，结合当前二次元主题调色并处理 CJK 双宽截断
func RenderTable(w io.Writer, cfg TableConfig) string {
	if len(cfg.Headers) == 0 && len(cfg.Rows) == 0 {
		return ""
	}

	colorOn := ColorEnabled(w)
	if cfg.Color != nil {
		colorOn = *cfg.Color
	}
	asciiOn := ASCIIOnly()
	if cfg.ASCII != nil {
		asciiOn = *cfg.ASCII
	}

	maxWidth := cfg.MaxWidth
	if maxWidth <= 0 {
		maxWidth = resolveTableMaxWidth(w)
	}

	headers, rows := normalizeTableData(cfg.Headers, cfg.Rows)
	r := NewRenderer(w, &colorOn)

	border := lipgloss.RoundedBorder()
	if asciiOn {
		border = lipgloss.ASCIIBorder()
	}

	innerMax := 0
	if maxWidth > 2 {
		innerMax = maxWidth - 2
	}

	inner := buildLipglossTable(r, border, headers, rows, cfg.Alignments, cfg.HeaderAlign, 0, true).String()
	if innerMax > 0 && lipgloss.Width(inner) > innerMax {
		inner = buildLipglossTable(r, border, headers, rows, cfg.Alignments, cfg.HeaderAlign, innerMax, false).String()
	}

	p := ActivePalette()
	card := r.NewStyle().
		Border(border).
		BorderForeground(p.Hairline).
		Render(inner)
	if asciiOn && strings.ContainsRune(card, '…') {
		card = replaceEllipsisASCIIPreserveWidth(card)
	}
	return card + "\n"
}

// replaceEllipsisASCIIPreserveWidth 将截断产生的单列 '…' 替换为等宽 "..."，并跳过 ANSI 转义序列
func replaceEllipsisASCIIPreserveWidth(s string) string {
	runes := []rune(s)
	out := make([]rune, 0, len(runes))
	vis := make([]bool, 0, len(runes))

	const (
		stateNormal = iota
		stateESC
		stateCSI
		stateOSC
		stateOSCESC
	)
	state := stateNormal

	for _, r := range runes {
		switch state {
		case stateESC:
			out = append(out, r)
			vis = append(vis, false)
			switch r {
			case '[':
				state = stateCSI
			case ']':
				state = stateOSC
			default:
				state = stateNormal
			}
			continue
		case stateCSI:
			out = append(out, r)
			vis = append(vis, false)
			if r >= 0x40 && r <= 0x7E {
				state = stateNormal
			}
			continue
		case stateOSC:
			out = append(out, r)
			vis = append(vis, false)
			if r == '\x07' {
				state = stateNormal
			} else if r == '\x1b' {
				state = stateOSCESC
			}
			continue
		case stateOSCESC:
			out = append(out, r)
			vis = append(vis, false)
			state = stateNormal
			continue
		}

		if r == '\x1b' {
			out = append(out, r)
			vis = append(vis, false)
			state = stateESC
			continue
		}

		if r != '…' {
			out = append(out, r)
			vis = append(vis, isReplaceableTailRune(r))
			continue
		}

		n := len(out)
		if n >= 2 && vis[n-1] && vis[n-2] &&
			lipgloss.Width(string(out[n-1])) == 1 && lipgloss.Width(string(out[n-2])) == 1 {
			out = append(out[:n-2], '.', '.', '.')
			vis = append(vis[:n-2], true, true, true)
			continue
		}
		if n >= 1 && vis[n-1] && lipgloss.Width(string(out[n-1])) == 2 {
			out = append(out[:n-1], '.', '.', '.')
			vis = append(vis[:n-1], true, true, true)
			continue
		}
		if n >= 1 && vis[n-1] && lipgloss.Width(string(out[n-1])) == 1 {
			out = append(out[:n-1], '.', '.')
			vis = append(vis[:n-1], true, true)
			continue
		}
		out = append(out, '.')
		vis = append(vis, true)
	}
	return string(out)
}

func isReplaceableTailRune(r rune) bool {
	return r > ' ' && r != '|' && r != '+' && r != '\x1b'
}

func buildLipglossTable(r *lipgloss.Renderer, border lipgloss.Border, headers []string, rows [][]string, aligns []TextAlign, headerAlign *TextAlign, width int, wrap bool) *table.Table {
	p := ActivePalette()
	headerStyle := r.NewStyle().
		Foreground(p.Primary).
		Bold(true).
		Padding(0, 1)

	borderStyle := r.NewStyle().
		Foreground(p.Hairline)

	indexStyle := r.NewStyle().
		Foreground(p.Muted).
		Padding(0, 1)

	cellStyle := r.NewStyle().
		Padding(0, 1)

	t := table.New().
		Border(border).
		BorderTop(false).
		BorderBottom(len(headers) == 0).
		BorderLeft(false).
		BorderRight(false).
		BorderHeader(len(headers) > 0).
		BorderColumn(false).
		BorderRow(false).
		BorderStyle(borderStyle).
		Wrap(wrap).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			var st lipgloss.Style
			switch {
			case row == table.HeaderRow:
				st = headerStyle
				if headerAlign != nil {
					st = st.Align(toLipglossAlign(*headerAlign))
				}
			case col == 0:
				st = indexStyle
			default:
				st = cellStyle
			}
			if row != table.HeaderRow && col >= 0 && col < len(aligns) {
				st = st.Align(toLipglossAlign(aligns[col]))
			}
			return st
		})

	if len(headers) > 0 {
		t.Headers(headers...)
	}
	if width > 0 {
		t.Width(width)
	}
	return t
}

func resolveTableMaxWidth(w io.Writer) int {
	if IsTTY(w) {
		return TerminalWidth()
	}
	if IsTTY(os.Stdout) {
		return TerminalWidth()
	}
	tw := TerminalWidth()
	if tw > 120 {
		return tw
	}
	return 120
}

func normalizeTableData(headers []string, rows [][]string) ([]string, [][]string) {
	cols := len(headers)
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	var outHeaders []string
	if len(headers) > 0 {
		outHeaders = make([]string, cols)
		for i := range headers {
			outHeaders[i] = sanitizeCell(headers[i])
		}
	}

	outRows := make([][]string, len(rows))
	for i, r := range rows {
		nr := make([]string, cols)
		for j := range r {
			nr[j] = sanitizeCell(r[j])
		}
		outRows[i] = nr
	}
	return outHeaders, outRows
}

func sanitizeCell(s string) string {
	return strings.ReplaceAll(s, "\r", "")
}

func toLipglossAlign(a TextAlign) lipgloss.Position {
	switch a {
	case AlignCenter:
		return lipgloss.Center
	case AlignRight:
		return lipgloss.Right
	default:
		return lipgloss.Left
	}
}
