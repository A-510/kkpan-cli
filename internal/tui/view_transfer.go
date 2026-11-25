package tui

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/kkpan-cli/kkpan/internal/ui"
)

func (m *Model) renderTransferBody(r *lipgloss.Renderer, p ui.ThemePalette, totalW int, colorOn, asciiOn bool) string {
	border := lipgloss.RoundedBorder()
	if asciiOn {
		border = lipgloss.ASCIIBorder()
	}

	vpRows := m.listViewportRows()
	showInspector := totalW >= 94

	listInnerW := totalW - 4
	if listInnerW < 10 {
		listInnerW = 10
	}
	inspectorInnerW := 0
	if showInspector {
		inspectorInnerW = totalW*36/100 - 4
		if inspectorInnerW < 28 {
			inspectorInnerW = 28
		}
		listInnerW = totalW - inspectorInnerW - 8
	}

	listPane := m.renderTransferListPane(r, p, listInnerW, vpRows, colorOn, asciiOn)
	leftBox := r.NewStyle().
		Border(border).
		BorderForeground(p.Hairline).
		Padding(0, 1).
		Width(listInnerW + 2).
		Height(vpRows + 1).
		Render(listPane)

	if !showInspector {
		return leftBox
	}

	rightPane := m.renderTransferInspectorPane(r, p, inspectorInnerW, vpRows, colorOn, asciiOn)
	rightBox := r.NewStyle().
		Border(border).
		BorderForeground(p.Hairline).
		Padding(0, 1).
		Width(inspectorInnerW + 2).
		Height(vpRows + 1).
		Render(rightPane)

	return lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)
}

func (m *Model) renderTransferListPane(r *lipgloss.Renderer, p ui.ThemePalette, innerW, totalRows int, colorOn, asciiOn bool) string {
	muted := r.NewStyle().Foreground(p.Muted)
	titleStyle := r.NewStyle().Foreground(p.Primary).Bold(true)
	ellipsis := "…"
	if asciiOn {
		ellipsis = "..."
	}

	// 行 1：标题 + 并发策略与总吞吐率汇总
	sum := m.transferSummary
	limitTag := fmt.Sprintf("并行:%d", sum.DownloadLimit)
	if sum.DownloadLimit == 0 {
		limitTag = "并行:全部"
	}
	speedSummary := limitTag
	if sum.ActiveDownload+sum.ActiveUpload > 0 {
		speedSummary = fmt.Sprintf("%s · 进行中 %d · 排队 %d · %s/s",
			limitTag, sum.ActiveDownload+sum.ActiveUpload, sum.Queued, ui.FormatBytes(int64(sum.TotalBytesPerSec)))
	} else if sum.Total > 0 {
		speedSummary = fmt.Sprintf("%s · 总计 %d · 完成 %d · 失败 %d", limitTag, sum.Total, sum.Done, sum.Failed)
	}
	headLeft := titleStyle.Render(" TRANSFER // 传输星港")
	headRight := muted.Render(speedSummary)
	headGap := innerW - lipgloss.Width(headLeft) - lipgloss.Width(headRight)
	headLine := headLeft
	if headGap >= 2 && speedSummary != "" {
		headLine += strings.Repeat(" ", headGap) + headRight
	} else if speedSummary != "" {
		headLine += "  " + headRight
	}

	// 行 2：筛选页签栏 [1]全部 [2]进行中 [3]已完成 [4]失败/取消
	tabs := []struct {
		filter TransferFilter
		label  string
		count  int
	}{
		{FilterAll, "1:全部", sum.Total},
		{FilterActive, "2:进行中", sum.ActiveDownload + sum.ActiveUpload + sum.Queued},
		{FilterDone, "3:已完成", sum.Done},
		{FilterFailed, "4:失败/取消", sum.Total - sum.Done - (sum.ActiveDownload + sum.ActiveUpload + sum.Queued)},
	}
	var tabParts []string
	for _, tb := range tabs {
		txt := fmt.Sprintf("[%s(%d)]", tb.label, maxInt(0, tb.count))
		if tb.filter == m.transferFilter {
			tabParts = append(tabParts, r.NewStyle().Foreground(p.Accent).Bold(true).Render(txt))
		} else {
			tabParts = append(tabParts, muted.Render(txt))
		}
	}
	tabLine := " " + ansi.Truncate(strings.Join(tabParts, " "), innerW, ellipsis)

	lines := make([]string, 0, totalRows+1)
	lines = append(lines, ansi.Truncate(headLine, innerW, ellipsis), tabLine)

	filtered := m.filteredTransferTasks()
	vpRows := m.transferViewportRows()

	if len(filtered) == 0 {
		emptyHint := "  ( ･ω･)ﾉ 传输星港当前静悄悄的 —— 回档案按 [d] 下载或按 [u] 上传"
		if len(m.transferTasks) > 0 {
			emptyHint = "  (・_・;) 当前筛选分类下暂无传输任务，按 1-4 切换分类"
		}
		lines = append(lines, "", muted.Render(ansi.Truncate(emptyHint, innerW, ellipsis)))
		for len(lines) < totalRows+1 {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}

	end := m.transferScroll + vpRows
	if end > len(filtered) {
		end = len(filtered)
	}

	barW := 12
	if innerW < 64 {
		barW = 8
	}
	if innerW < 48 {
		barW = 0
	}
	rateW := 18
	if innerW < 56 {
		rateW = 11
	}

	fixedW := 2 + 3 + 1 + 2 + 1 + 4 + 1 // 光标(2)+编号(3)+空格+方向(2)+空格+状态(4)+空格
	if barW > 0 {
		fixedW += 1 + barW + 1 + 4 // 进度条 + 百分比
	}
	fixedW += 1 + rateW
	nameW := innerW - fixedW
	if nameW < 10 {
		nameW = 10
	}

	for i := m.transferScroll; i < end; i++ {
		t := filtered[i]
		isCur := i == m.transferCursor

		curMark := "  "
		if isCur {
			if asciiOn {
				curMark = "> "
			} else {
				curMark = "▎ "
			}
		}

		idxLabel := fmt.Sprintf("#%02d", t.ID)
		dirGlyph := transferDirGlyph(t.Kind, asciiOn)
		statusText, statusColor := transferStatusLabel(t.Status, p)
		dispName := padRightVisible(ansi.Truncate(sanitizeOneLine(t.Name), nameW, ellipsis), nameW)

		ratio := transferRatio(t)
		pctStr := fmt.Sprintf("%3d%%", int(ratio*100+0.5))
		rateStr := formatTransferMetric(t)

		var rowPlain strings.Builder
		rowPlain.WriteString(curMark)
		rowPlain.WriteString(padRightVisible(idxLabel, 3))
		rowPlain.WriteString(" ")
		rowPlain.WriteString(dirGlyph)
		rowPlain.WriteString(" ")
		rowPlain.WriteString(padRightVisible(statusText, 4))
		rowPlain.WriteString(" ")
		rowPlain.WriteString(dispName)
		if barW > 0 {
			rowPlain.WriteString(" ")
			rowPlain.WriteString(renderTransferBarPlain(ratio, barW, asciiOn))
			rowPlain.WriteString(" ")
			rowPlain.WriteString(pctStr)
		}
		rowPlain.WriteString(" ")
		rowPlain.WriteString(padLeftVisible(ansi.Truncate(rateStr, rateW, ellipsis), rateW))

		rawRow := ansi.Truncate(rowPlain.String(), innerW, ellipsis)
		if !colorOn {
			lines = append(lines, rawRow)
			continue
		}
		if isCur {
			styled := r.NewStyle().
				Background(p.RowSelectBg).
				Foreground(p.Ink).
				Bold(true).
				Width(innerW).
				Render(rawRow)
			lines = append(lines, styled)
			continue
		}

		dirColor := p.Secondary
		if t.Kind == TransferUpload {
			dirColor = p.Highlight
		}
		var colored strings.Builder
		colored.WriteString(curMark)
		colored.WriteString(muted.Render(padRightVisible(idxLabel, 3)))
		colored.WriteString(" ")
		colored.WriteString(r.NewStyle().Foreground(dirColor).Bold(true).Render(dirGlyph))
		colored.WriteString(" ")
		colored.WriteString(r.NewStyle().Foreground(statusColor).Bold(true).Render(padRightVisible(statusText, 4)))
		colored.WriteString(" ")
		colored.WriteString(r.NewStyle().Foreground(p.Ink).Render(dispName))
		if barW > 0 {
			colored.WriteString(" ")
			colored.WriteString(renderTransferBarStyled(r, p, t.Status, ratio, barW, asciiOn))
			colored.WriteString(" ")
			colored.WriteString(muted.Render(pctStr))
		}
		colored.WriteString(" ")
		colored.WriteString(muted.Render(padLeftVisible(ansi.Truncate(rateStr, rateW, ellipsis), rateW)))
		lines = append(lines, ansi.Truncate(colored.String(), innerW, ellipsis))
	}

	for len(lines) < totalRows+1 {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderTransferInspectorPane(r *lipgloss.Renderer, p ui.ThemePalette, innerW, totalRows int, colorOn, asciiOn bool) string {
	muted := r.NewStyle().Foreground(p.Muted)
	titleStyle := r.NewStyle().Foreground(p.Accent).Bold(true)
	ink := r.NewStyle().Foreground(p.Ink).Bold(true)
	sec := r.NewStyle().Foreground(p.Secondary)
	ellipsis := "…"
	if asciiOn {
		ellipsis = "..."
	}

	kaomoji := "( ･ω･)ﾉ"
	if m.transferSummary.ActiveDownload+m.transferSummary.ActiveUpload > 0 {
		kaomoji = "✦ 传输中…"
	} else if m.transferSummary.Failed > 0 {
		kaomoji = "(x_x) 存在异常"
	}

	headLeft := titleStyle.Render("INSPECTOR // 任务详览")
	headRight := muted.Render(kaomoji)
	gap := innerW - lipgloss.Width(headLeft) - lipgloss.Width(headRight)
	topLine := headLeft
	if gap >= 1 {
		topLine += strings.Repeat(" ", gap) + headRight
	}

	lines := []string{ansi.Truncate(topLine, innerW, ellipsis)}
	cur := m.currentTransferTask()
	if cur == nil {
		lines = append(lines, "", muted.Render("当前没有选中的传输任务"), "", muted.Render("按 [Tab] 返回档案浏览选择文件"))
	} else {
		dirLabel := "下载 (云端 -> 本地)"
		if cur.Kind == TransferUpload {
			dirLabel = "上传 (本地 -> 云端)"
		}
		statusLabel, statusColor := transferStatusLabel(cur.Status, p)
		lines = append(lines,
			r.NewStyle().Foreground(p.Hairline).Render(strings.Repeat("─", innerW)),
			muted.Render(fmt.Sprintf("TASK #%02d · %s", cur.ID, dirLabel)),
			ink.Render(ansi.Truncate(sanitizeOneLine(cur.Name), innerW, ellipsis)),
			"",
		)

		addKV := func(k, v string, valColor lipgloss.Color) {
			label := muted.Render(padRightVisible(k, 7))
			valBudget := maxInt(6, innerW-8)
			val := ansi.Truncate(sanitizeOneLine(v), valBudget, ellipsis)
			lines = append(lines, label+" "+r.NewStyle().Foreground(valColor).Render(val))
		}

		addKV("状态:", statusLabel, statusColor)
		progressText := fmt.Sprintf("%s / %s", ui.FormatBytes(cur.Completed), ui.FormatBytes(cur.Total))
		if cur.Total <= 0 {
			progressText = ui.FormatBytes(cur.Completed)
		}
		addKV("进度:", progressText, p.Ink)
		if cur.Status == StatusRunning {
			addKV("速率:", ui.FormatBytes(int64(cur.BytesPerSec))+"/s", p.Accent)
		} else if !cur.StartedAt.IsZero() && !cur.FinishedAt.IsZero() {
			addKV("耗时:", cur.FinishedAt.Sub(cur.StartedAt).Round(100*time.Millisecond).String(), p.Secondary)
		}
		addKV("来源:", cur.Source, p.Secondary)
		addKV("目标:", cur.Target, p.Secondary)
		addKV("建立:", cur.CreatedAt.Format("15:04:05"), p.Muted)

		if cur.Error != "" {
			lines = append(lines, "", r.NewStyle().Foreground(p.Fail).Bold(true).Render("错误原因:"))
			for _, wLine := range wrapTextByWidth(sanitizeOneLine(cur.Error), maxInt(10, innerW)) {
				lines = append(lines, r.NewStyle().Foreground(p.Fail).Render(wLine))
			}
		}
		if cur.Hint != "" {
			lines = append(lines, "", r.NewStyle().Foreground(p.Warn).Bold(true).Render("排错建议:"))
			for _, wLine := range wrapTextByWidth(sanitizeOneLine(cur.Hint), maxInt(10, innerW)) {
				lines = append(lines, sec.Render(wLine))
			}
		}
	}

	if len(lines) > totalRows+1 {
		lines = lines[:totalRows+1]
	}
	for len(lines) < totalRows+1 {
		lines = append(lines, "")
	}
	_ = colorOn
	return strings.Join(lines, "\n")
}

func (m *Model) renderTransferHeaderBadge(r *lipgloss.Renderer, p ui.ThemePalette, colorOn, asciiOn bool) string {
	s := m.transferSummary
	if s.Total == 0 {
		return ""
	}
	dnSym, upSym := "↓", "↑"
	if asciiOn {
		dnSym, upSym = "DN:", "UP:"
	}

	activeTotal := s.ActiveDownload + s.ActiveUpload
	if activeTotal > 0 || s.Queued > 0 {
		var parts []string
		if s.ActiveDownload > 0 {
			parts = append(parts, fmt.Sprintf("%s%d", dnSym, s.ActiveDownload))
		}
		if s.ActiveUpload > 0 {
			parts = append(parts, fmt.Sprintf("%s%d", upSym, s.ActiveUpload))
		}
		if s.Queued > 0 {
			parts = append(parts, fmt.Sprintf("排队%d", s.Queued))
		}
		if s.TotalBytesPerSec > 0 {
			parts = append(parts, ui.FormatBytes(int64(s.TotalBytesPerSec))+"/s")
		}
		return ui.RenderBadge(os.Stdout, ui.BadgeInfo, strings.Join(parts, " "), colorOn)
	}
	if s.Failed > 0 {
		return ui.RenderBadge(os.Stdout, ui.BadgeFail, fmt.Sprintf("传输异常:%d", s.Failed), colorOn)
	}
	return r.NewStyle().Foreground(p.Success).Render(fmt.Sprintf("传输完成:%d", s.Done))
}

func transferDirGlyph(kind TransferKind, asciiOn bool) string {
	if kind == TransferDownload {
		if asciiOn {
			return "v "
		}
		return "↓ "
	}
	if asciiOn {
		return "^ "
	}
	return "↑ "
}

func transferStatusLabel(st TransferStatus, p ui.ThemePalette) (string, lipgloss.Color) {
	switch st {
	case StatusQueued:
		return "排队", p.Muted
	case StatusRunning:
		return "传输", p.Accent
	case StatusDone:
		return "完成", p.Success
	case StatusRapid:
		return "秒传", p.Success
	case StatusSkipped:
		return "跳过", p.Warn
	case StatusFailed:
		return "失败", p.Fail
	case StatusCanceled:
		return "取消", p.Warn
	default:
		return "-", p.Muted
	}
}

func transferRatio(t TransferTask) float64 {
	if t.Status.IsSuccess() {
		return 1
	}
	if t.Total <= 0 {
		return 0
	}
	r := float64(t.Completed) / float64(t.Total)
	if r < 0 {
		return 0
	}
	if r > 1 {
		return 1
	}
	return r
}

func formatTransferMetric(t TransferTask) string {
	switch t.Status {
	case StatusRunning:
		if t.BytesPerSec > 0 {
			return ui.FormatBytes(int64(t.BytesPerSec)) + "/s"
		}
		return ui.FormatBytes(t.Completed)
	case StatusQueued:
		if t.Total > 0 {
			return "等待 · " + ui.FormatBytes(t.Total)
		}
		return "等待中"
	case StatusDone, StatusRapid, StatusSkipped:
		return ui.FormatBytes(t.Total)
	case StatusFailed:
		return "错误"
	case StatusCanceled:
		return "已取消"
	default:
		return "-"
	}
}

func renderTransferBarPlain(ratio float64, width int, ascii bool) string {
	if width <= 0 {
		return ""
	}
	filled := int(ratio * float64(width))
	if filled > width {
		filled = width
	}
	fillCh, headCh, emptyCh := "━", "◈", "─"
	if ascii {
		fillCh, headCh, emptyCh = "=", ">", "-"
	}
	if filled <= 0 {
		return strings.Repeat(emptyCh, width)
	}
	if filled >= width {
		return strings.Repeat(fillCh, width-1) + headCh
	}
	return strings.Repeat(fillCh, filled-1) + headCh + strings.Repeat(emptyCh, width-filled)
}

func renderTransferBarStyled(r *lipgloss.Renderer, p ui.ThemePalette, st TransferStatus, ratio float64, width int, ascii bool) string {
	if width <= 0 {
		return ""
	}
	filled := int(ratio * float64(width))
	if filled > width {
		filled = width
	}
	fillCh, headCh, emptyCh := "━", "◈", "─"
	if ascii {
		fillCh, headCh, emptyCh = "=", ">", "-"
	}

	barColor := p.Accent
	switch {
	case st.IsSuccess():
		barColor = p.Success
	case st == StatusFailed:
		barColor = p.Fail
	case st == StatusCanceled || st == StatusQueued:
		barColor = p.Muted
	}

	if filled <= 0 {
		return r.NewStyle().Foreground(p.Hairline).Render(strings.Repeat(emptyCh, width))
	}
	var left, head, right string
	if filled >= width {
		left = strings.Repeat(fillCh, width-1)
		head = headCh
	} else {
		left = strings.Repeat(fillCh, filled-1)
		head = headCh
		right = strings.Repeat(emptyCh, width-filled)
	}
	return r.NewStyle().Foreground(barColor).Render(left+head) + r.NewStyle().Foreground(p.Hairline).Render(right)
}

func wrapTextByWidth(text string, maxW int) []string {
	if maxW <= 4 || text == "" {
		return []string{text}
	}
	var lines []string
	var cur strings.Builder
	curW := 0
	for _, ru := range text {
		rw := lipgloss.Width(string(ru))
		if curW+rw > maxW && cur.Len() > 0 {
			lines = append(lines, cur.String())
			cur.Reset()
			curW = 0
		}
		cur.WriteRune(ru)
		curW += rw
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
