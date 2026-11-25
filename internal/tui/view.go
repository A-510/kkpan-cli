package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
)

// View 渲染遵循 DESIGN.md 日式二次元「星海档案图鉴」规范的全屏终端界面
func (m *Model) View() string {
	colorOn := ui.ColorEnabled(os.Stdout)
	asciiOn := ui.ASCIIOnly()
	r := ui.NewRenderer(os.Stdout, &colorOn)
	p := ui.ThemeByName(m.theme)

	w := m.width
	if w <= 0 {
		w = 100
	}

	header := m.renderHeader(r, p, w, colorOn, asciiOn)
	footer := m.renderFooter(r, p, w, colorOn, asciiOn)

	if m.mode == modeHelp {
		return lipgloss.JoinVertical(lipgloss.Left, header, m.renderHelpCard(r, p, w, asciiOn), footer)
	}
	var body string
	if m.view == viewTransfer {
		body = m.renderTransferBody(r, p, w, colorOn, asciiOn)
	} else {
		body = m.renderBody(r, p, w, colorOn, asciiOn)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, body, footer)
}

func (m *Model) renderHeader(r *lipgloss.Renderer, p ui.ThemePalette, totalW int, colorOn, asciiOn bool) string {
	star := "✦"
	dot := "●"
	pathMark := "❖"
	ellipsis := "…"
	if asciiOn {
		star = "*"
		dot = "*"
		pathMark = ">"
		ellipsis = "..."
	}

	brand := r.NewStyle().Foreground(p.Accent).Bold(true).Render(star + " KK-PAN // 星海档案")
	muted := r.NewStyle().Foreground(p.Muted)
	sec := r.NewStyle().Foreground(p.Secondary).Bold(true)
	themeTag := muted.Render("[" + p.BadgeTag + "]")
	viewTag := muted.Render("[档案]")
	if m.view == viewTransfer {
		viewTag = r.NewStyle().Foreground(p.Highlight).Bold(true).Render("[传输星港]")
	}

	userPart := ""
	if nick := sanitizeOneLine(m.nickname); nick != "" {
		userPart = r.NewStyle().Foreground(p.Success).Render(dot) + " " +
			r.NewStyle().Foreground(p.Ink).Bold(true).Render(nick)
		if m.memberType != "" {
			kind := ui.BadgeNeutral
			if strings.Contains(strings.ToUpper(m.memberType), "VIP") {
				kind = ui.BadgeInfo
			}
			userPart += " " + ui.RenderBadge(os.Stdout, kind, m.memberType, colorOn)
		}
	}

	quotaPart := ""
	if m.hasQuota && m.quotaTotal > 0 {
		ratio := float64(m.quotaUsed) / float64(m.quotaTotal)
		bar := ui.RenderQuotaBar(os.Stdout, ratio, 10, colorOn, asciiOn)
		quotaPart = fmt.Sprintf("%s %s",
			bar,
			muted.Render(fmt.Sprintf("%.1f%% (%s/%s)", ratio*100, ui.FormatBytes(m.quotaUsed), ui.FormatBytes(m.quotaTotal))),
		)
	}

	topLeft := brand + " " + themeTag + " " + viewTag
	if userPart != "" {
		topLeft += "  " + userPart
	}

	maxLineW := maxInt(10, totalW-2)
	gap1 := totalW - lipgloss.Width(topLeft) - lipgloss.Width(quotaPart) - 2
	var line1Body string
	if gap1 >= 2 && quotaPart != "" {
		line1Body = topLeft + strings.Repeat(" ", gap1) + quotaPart
	} else if quotaPart != "" {
		line1Body = topLeft + "  " + quotaPart
	} else {
		line1Body = topLeft
	}
	line1 := " " + ansi.Truncate(line1Body, maxLineW, ellipsis)

	// 第二行：票券式路径 + 排序 / 过滤 / 勾选态
	pathBudget := maxInt(10, totalW-38)
	pathPart := r.NewStyle().Foreground(p.Secondary).Render(pathMark) + " " +
		sec.Render(ui.TruncateMiddleASCII(sanitizeOneLine(m.cwd), pathBudget, asciiOn))

	var tags []string
	if txBadge := m.renderTransferHeaderBadge(r, p, colorOn, asciiOn); txBadge != "" {
		tags = append(tags, txBadge)
	}
	if m.view == viewArchive {
		if len(m.selected) > 0 {
			tags = append(tags, r.NewStyle().Foreground(p.Highlight).Bold(true).Render(fmt.Sprintf("已选 %d", len(m.selected))))
		}
		if m.filterQuery != "" {
			tags = append(tags, r.NewStyle().Foreground(p.Warn).Render("过滤:"+sanitizeOneLine(m.filterQuery)))
		}
		tags = append(tags, muted.Render("排序:")+r.NewStyle().Foreground(p.Accent).Render(sortPresetLabel(m.sortIdx, asciiOn)))
	}
	rightPart := strings.Join(tags, "  ")

	gap2 := totalW - lipgloss.Width(pathPart) - lipgloss.Width(rightPart) - 2
	var line2Body string
	if gap2 >= 2 {
		line2Body = pathPart + strings.Repeat(" ", gap2) + rightPart
	} else {
		line2Body = pathPart + "  " + rightPart
	}
	line2 := " " + ansi.Truncate(line2Body, maxLineW, ellipsis)

	return line1 + "\n" + line2
}

func (m *Model) renderBody(r *lipgloss.Renderer, p ui.ThemePalette, totalW int, colorOn, asciiOn bool) string {
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

	listPane := m.renderListPane(r, p, listInnerW, vpRows, colorOn, asciiOn)
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

	rightPane := m.renderInspectorPane(r, p, inspectorInnerW, vpRows, colorOn, asciiOn)
	rightBox := r.NewStyle().
		Border(border).
		BorderForeground(p.Hairline).
		Padding(0, 1).
		Width(inspectorInnerW + 2).
		Height(vpRows + 1).
		Render(rightPane)

	return lipgloss.JoinHorizontal(lipgloss.Top, leftBox, rightBox)
}

func (m *Model) renderListPane(r *lipgloss.Renderer, p ui.ThemePalette, innerW, vpRows int, colorOn, asciiOn bool) string {
	muted := r.NewStyle().Foreground(p.Muted)
	titleStyle := r.NewStyle().Foreground(p.Primary).Bold(true)
	ellipsis := "…"
	if asciiOn {
		ellipsis = "..."
	}

	idxW := 3
	sizeW := 10
	dateW := 16
	if innerW < 60 {
		dateW = 0
	}
	if innerW < 38 {
		sizeW = 0
	}
	nameW := innerW - idxW - 5 // 光标符(1)+勾选符(1)+类型符(2)+间距
	if sizeW > 0 {
		nameW -= 1 + sizeW
	}
	if dateW > 0 {
		nameW -= 1 + dateW
	}
	if nameW < 8 {
		nameW = 8
	}

	headerLine := fmt.Sprintf(" %-*s  %-*s", idxW, "NO.", nameW, "ARCHIVE // 档案名称")
	if sizeW > 0 {
		headerLine += fmt.Sprintf(" %*s", sizeW, "体积")
	}
	if dateW > 0 {
		headerLine += fmt.Sprintf(" %-*s", dateW, "修改时间")
	}
	lines := make([]string, 0, vpRows+1)
	lines = append(lines, titleStyle.Render(ansi.Truncate(headerLine, innerW, ellipsis)))

	if m.loading && len(m.visible) == 0 {
		lines = append(lines, muted.Render("  ✦ 正在同步星海目录数据..."))
		for len(lines) < vpRows+1 {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}

	if len(m.visible) == 0 {
		emptyHint := "  (・_・;) 当前目录空空如也"
		if m.filterQuery != "" {
			emptyHint = "  (・_・;) 没有匹配 \"" + sanitizeOneLine(m.filterQuery) + "\" 的档案"
		}
		lines = append(lines, muted.Render(ansi.Truncate(emptyHint, innerW, ellipsis)))
		for len(lines) < vpRows+1 {
			lines = append(lines, "")
		}
		return strings.Join(lines, "\n")
	}

	end := m.scrollTop + vpRows
	if end > len(m.visible) {
		end = len(m.visible)
	}

	for i := m.scrollTop; i < end; i++ {
		it := m.visible[i]
		isCur := i == m.cursor
		_, isSel := m.selected[it.FID]

		curMark := " "
		if isCur {
			if asciiOn {
				curMark = ">"
			} else {
				curMark = "▎"
			}
		}

		selMark := " "
		if isSel {
			if asciiOn {
				selMark = "*"
			} else {
				selMark = "✦"
			}
		}

		typeMark := animeFileGlyph(it, asciiOn)
		dispName := sanitizeOneLine(it.FileName)
		if it.IsDir {
			dispName += "/"
		}
		dispName = ansi.Truncate(dispName, nameW, ellipsis)
		paddedName := padRightVisible(dispName, nameW)

		szStr := ui.FormatBytes(it.Size)
		if it.IsDir {
			szStr = "-"
		}
		dateStr := "-"
		if it.UpdatedAt > 0 {
			ts := it.UpdatedAt
			if ts > 1e12 {
				ts /= 1000
			}
			dateStr = time.Unix(ts, 0).Format("2006-01-02 15:04")
		}

		idxLabel := fmt.Sprintf("%02d", i+1)

		var rowPlain strings.Builder
		rowPlain.WriteString(curMark)
		rowPlain.WriteString(selMark)
		rowPlain.WriteString(" ")
		rowPlain.WriteString(padRightVisible(idxLabel, idxW))
		rowPlain.WriteString(" ")
		rowPlain.WriteString(typeMark)
		rowPlain.WriteString(" ")
		rowPlain.WriteString(paddedName)
		if sizeW > 0 {
			rowPlain.WriteString(" ")
			rowPlain.WriteString(padLeftVisible(szStr, sizeW))
		}
		if dateW > 0 {
			rowPlain.WriteString(" ")
			rowPlain.WriteString(padRightVisible(dateStr, dateW))
		}

		rawRow := ansi.Truncate(rowPlain.String(), innerW, ellipsis)
		if !colorOn {
			lines = append(lines, rawRow)
			continue
		}

		if isCur {
			// 当前行采用左竖条高亮 + 柔和主题选中底色，确保文字高对比清晰
			styled := r.NewStyle().
				Background(p.RowSelectBg).
				Foreground(p.Ink).
				Bold(true).
				Width(innerW).
				Render(rawRow)
			lines = append(lines, styled)
			continue
		}

		// 非选中行：保持底色透明，仅对索引/符号轻度着色
		idxPart := muted.Render(padRightVisible(idxLabel, idxW))
		selStyled := " "
		if isSel {
			selStyled = r.NewStyle().Foreground(p.Highlight).Bold(true).Render(selMark)
		}
		glyphColor := p.Muted
		nameColor := p.Ink
		if it.IsDir {
			glyphColor = p.Secondary
			nameColor = p.Secondary
		}
		if isSel {
			nameColor = p.Highlight
		}

		var colored strings.Builder
		colored.WriteString(curMark)
		colored.WriteString(selStyled)
		colored.WriteString(" ")
		colored.WriteString(idxPart)
		colored.WriteString(" ")
		colored.WriteString(r.NewStyle().Foreground(glyphColor).Render(typeMark))
		colored.WriteString(" ")
		colored.WriteString(r.NewStyle().Foreground(nameColor).Render(paddedName))
		if sizeW > 0 {
			colored.WriteString(" ")
			colored.WriteString(muted.Render(padLeftVisible(szStr, sizeW)))
		}
		if dateW > 0 {
			colored.WriteString(" ")
			colored.WriteString(muted.Render(padRightVisible(dateStr, dateW)))
		}
		lines = append(lines, ansi.Truncate(colored.String(), innerW, ellipsis))
	}

	for len(lines) < vpRows+1 {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

func (m *Model) renderInspectorPane(r *lipgloss.Renderer, p ui.ThemePalette, innerW, vpRows int, colorOn, asciiOn bool) string {
	muted := r.NewStyle().Foreground(p.Muted)
	titleStyle := r.NewStyle().Foreground(p.Accent).Bold(true)
	ink := r.NewStyle().Foreground(p.Ink).Bold(true)
	sec := r.NewStyle().Foreground(p.Secondary)
	ellipsis := "…"
	if asciiOn {
		ellipsis = "..."
	}

	// 右栏顶部轻量日式状态颜文字（克制且贴合状态）
	kaomoji := "( ･ω･)ﾉ"
	switch {
	case m.mode == modeConfirmDelete:
		kaomoji = "(!) 待确认"
	case m.loading:
		kaomoji = "✦ 同步中…"
	case len(m.visible) == 0:
		kaomoji = "(・_・;)"
	}

	headLeft := titleStyle.Render("INSPECTOR // 详览")
	headRight := muted.Render(kaomoji)
	gap := innerW - lipgloss.Width(headLeft) - lipgloss.Width(headRight)
	topLine := headLeft
	if gap >= 1 {
		topLine += strings.Repeat(" ", gap) + headRight
	}

	lines := []string{ansi.Truncate(topLine, innerW, ellipsis)}

	cur := m.currentItem()
	if cur == nil {
		lines = append(lines, "", muted.Render("未选中任何档案条目"), "", muted.Render("按 'u' 上传本地文件到此处"))
	} else {
		codeNum := fmt.Sprintf("CODEX #%02d", m.cursor+1)
		lines = append(lines,
			r.NewStyle().Foreground(p.Hairline).Render(strings.Repeat("─", innerW)),
			muted.Render(codeNum),
			ink.Render(ansi.Truncate(sanitizeOneLine(cur.FileName), innerW, ellipsis)),
			"",
		)

		kindLabel := "普通文件"
		sizeLabel := ui.FormatBytes(cur.Size)
		if cur.IsDir {
			kindLabel = "目录文件夹"
			sizeLabel = "-"
		} else if ext := strings.TrimPrefix(strings.ToUpper(filepath.Ext(cur.FileName)), "."); ext != "" {
			kindLabel = ext + " 文件"
		}

		addKV := func(k, v string) {
			label := muted.Render(padRightVisible(k, 7))
			valBudget := maxInt(6, innerW-8)
			val := ansi.Truncate(sanitizeOneLine(v), valBudget, ellipsis)
			lines = append(lines, label+" "+sec.Render(val))
		}

		addKV("类型:", kindLabel)
		addKV("大小:", sizeLabel)
		addKV("FID:", cur.FID)
		if cur.MD5 != "" {
			addKV("MD5:", cur.MD5)
		}
		if cur.UpdatedAt > 0 {
			ts := cur.UpdatedAt
			if ts > 1e12 {
				ts /= 1000
			}
			addKV("更新:", time.Unix(ts, 0).Format("2006-01-02 15:04:05"))
		}
		addKV("下载到:", m.saveDir)
	}

	if len(m.selected) > 0 {
		var totalSel int64
		for _, s := range m.selected {
			totalSel += s.Size
		}
		lines = append(lines,
			"",
			r.NewStyle().Foreground(p.Hairline).Render(strings.Repeat("─", innerW)),
			r.NewStyle().Foreground(p.Highlight).Bold(true).Render(fmt.Sprintf("已勾选 %d 项 · 共 %s", len(m.selected), ui.FormatBytes(totalSel))),
		)
	}

	if len(lines) > vpRows+1 {
		lines = lines[:vpRows+1]
	}
	for len(lines) < vpRows+1 {
		lines = append(lines, "")
	}
	_ = colorOn
	return strings.Join(lines, "\n")
}

func (m *Model) renderFooter(r *lipgloss.Renderer, p ui.ThemePalette, totalW int, colorOn, asciiOn bool) string {
	ellipsis := "…"
	if asciiOn {
		ellipsis = "..."
	}
	maxW := maxInt(10, totalW-2)

	var line1 string
	switch m.mode {
	case modeFilter:
		line1 = r.NewStyle().Foreground(p.Warn).Bold(true).Render(" / 过滤档案: ") +
			r.NewStyle().Foreground(p.Ink).Render(m.inputValue+"_") +
			r.NewStyle().Foreground(p.Muted).Render("  (Enter 确认 · Esc 清除)")
	case modeInputMkdir:
		line1 = r.NewStyle().Foreground(p.Accent).Bold(true).Render(" + 新建目录: ") +
			r.NewStyle().Foreground(p.Ink).Render(m.inputValue+"_") +
			r.NewStyle().Foreground(p.Muted).Render("  (Enter 创建 · Esc 取消)")
	case modeInputRename:
		line1 = r.NewStyle().Foreground(p.Accent).Bold(true).Render(" ~ 重命名为: ") +
			r.NewStyle().Foreground(p.Ink).Render(m.inputValue+"_") +
			r.NewStyle().Foreground(p.Muted).Render("  (Enter 确认 · Esc 取消)")
	case modeInputUpload:
		line1 = r.NewStyle().Foreground(p.Secondary).Bold(true).Render(" ^ 上传本地路径: ") +
			r.NewStyle().Foreground(p.Ink).Render(m.inputValue+"_") +
			r.NewStyle().Foreground(p.Muted).Render("  (Enter 开始上传 · Esc 取消)")
	case modeConfirmDelete:
		n := len(m.activeTargets())
		line1 = r.NewStyle().Foreground(p.Fail).Bold(true).Render(fmt.Sprintf(" ! 确认将 %d 项移入回收站？ [y] 确认 / [n] 取消", n))
	case modeConfirmQuit:
		activeCnt := m.transferSummary.ActiveDownload + m.transferSummary.ActiveUpload + m.transferSummary.Queued
		line1 = r.NewStyle().Foreground(p.Warn).Bold(true).Render(fmt.Sprintf(" ! 仍有 %d 个后台传输任务未完成，退出将中止它们！ [y] 确认退出 / [n] 继续传输", activeCnt))
	default:
		if m.statusMsg != "" {
			st := r.NewStyle().Foreground(p.Success)
			prefix := " ✦ "
			if asciiOn {
				prefix = " * "
			}
			if m.statusErr {
				st = r.NewStyle().Foreground(p.Fail).Bold(true)
				prefix = " x "
			}
			line1 = st.Render(prefix + sanitizeOneLine(m.statusMsg))
		} else if m.view == viewTransfer {
			line1 = r.NewStyle().Foreground(p.Muted).Render(fmt.Sprintf(" 传输任务 %d 项 · 按 [Tab] 返回档案浏览器继续选购", len(m.transferTasks)))
		} else {
			line1 = r.NewStyle().Foreground(p.Muted).Render(fmt.Sprintf(" 共 %d 项 · 主题: %s (按 t 切换 · Tab 查看传输中心)", len(m.visible), p.DisplayName))
		}
	}

	upDnKey, enterKey, backKey := "↑↓/jk", "↵/l", "h/⌫"
	if asciiOn {
		upDnKey, enterKey, backKey = "^v/jk", "Ent/l", "h/BS"
	}

	keys := []struct {
		k string
		v string
	}{
		{upDnKey, "移动"},
		{enterKey, "进入"},
		{backKey, "上级"},
		{"Space", "多选"},
		{"/", "过滤"},
		{"s", "排序"},
		{"d", "下载"},
		{"u", "上传"},
		{"Tab", "传输"},
		{"n", "新建"},
		{"r", "改名"},
		{"x", "删除"},
		{"t", "主题"},
		{"?", "帮助"},
		{"q", "退出"},
	}
	if m.view == viewTransfer {
		keys = []struct {
			k string
			v string
		}{
			{upDnKey, "选择"},
			{"1-4", "筛选"},
			{"+/-", "并发"},
			{"a", "全并发"},
			{"c", "取消"},
			{"r", "重试"},
			{"x", "移除"},
			{"C", "清已完成"},
			{"u", "上传"},
			{"Tab/Esc", "回档案"},
			{"t", "主题"},
			{"?", "帮助"},
			{"q", "退出"},
		}
	}

	keyStyle := r.NewStyle().Foreground(p.Accent).Bold(true)
	valStyle := r.NewStyle().Foreground(p.Muted)
	var parts []string
	for _, kv := range keys {
		parts = append(parts, keyStyle.Render("["+kv.k+"]")+valStyle.Render(kv.v))
	}
	barLine := " " + ansi.Truncate(strings.Join(parts, " "), maxW, ellipsis)
	_ = colorOn
	return ansi.Truncate(line1, maxW, ellipsis) + "\n" + barLine
}

func (m *Model) renderHelpCard(r *lipgloss.Renderer, p ui.ThemePalette, totalW int, asciiOn bool) string {
	border := lipgloss.RoundedBorder()
	if asciiOn {
		border = lipgloss.ASCIIBorder()
	}
	title := r.NewStyle().Foreground(p.Accent).Bold(true).Render("✦ 星海档案快捷键图鉴 (按 ? / Esc 返回)")
	muted := r.NewStyle().Foreground(p.Muted)
	keySt := r.NewStyle().Foreground(p.Secondary).Bold(true)

	rows := []string{
		title,
		"",
		fmt.Sprintf("%s  上下移动光标        %s  进入目录 / 返回上级", keySt.Render("↑↓ / j k"), keySt.Render("Enter l / h ⌫")),
		fmt.Sprintf("%s  跳到首项 / 末项     %s  勾选 / 全选当前列表", keySt.Render("g / G   "), keySt.Render("Space / a    ")),
		fmt.Sprintf("%s  实时模糊过滤        %s  循环切换排序方式", keySt.Render("/       "), keySt.Render("s            ")),
		fmt.Sprintf("%s  后台加入下载队列    %s  后台上传本地路径", keySt.Render("d       "), keySt.Render("u            ")),
		fmt.Sprintf("%s  切换传输星港界面    %s  取消/重试/清已完成 (传输页)", keySt.Render("Tab / T "), keySt.Render("c / r / x / C")),
		fmt.Sprintf("%s  实时增减下载并发数  %s  一键全部同时下载   (传输页)", keySt.Render("+ / -   "), keySt.Render("a            ")),
		fmt.Sprintf("%s  新建目录 / 重命名   %s  移入回收站（带确认）", keySt.Render("n / r   "), keySt.Render("x / Delete   ")),
		fmt.Sprintf("%s  强制刷新当前目录    %s  切换日式主题 (星穹/档案/物语)", keySt.Render("R       "), keySt.Render("t            ")),
		"",
		muted.Render("提示: 按 d/u 后文件将在后台持续传输，你可以立刻切换其他目录继续加入更多下载任务；按 Tab 随时查看总进度。"),
	}

	boxW := minInt(totalW-4, 84)
	if boxW < 40 {
		boxW = 40
	}
	return r.NewStyle().
		Border(border).
		BorderForeground(p.Accent).
		Padding(1, 2).
		Width(boxW).
		Render(strings.Join(rows, "\n"))
}

func animeFileGlyph(it quarkpan.FileItem, asciiOn bool) string {
	if it.IsDir {
		if asciiOn {
			return "[D]"
		}
		return "❖"
	}
	ext := strings.ToLower(filepath.Ext(it.FileName))
	switch ext {
	case ".mp4", ".mkv", ".avi", ".mov", ".flv", ".webm", ".ts":
		if asciiOn {
			return "[V]"
		}
		return "▶"
	case ".mp3", ".flac", ".wav", ".aac", ".ogg", ".m4a":
		if asciiOn {
			return "[A]"
		}
		return "♪"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".svg":
		if asciiOn {
			return "[I]"
		}
		return "◈"
	case ".zip", ".rar", ".7z", ".tar", ".gz", ".xz":
		if asciiOn {
			return "[Z]"
		}
		return "⬡"
	case ".pdf", ".doc", ".docx", ".md", ".txt", ".ass", ".srt":
		if asciiOn {
			return "[T]"
		}
		return "▤"
	default:
		if asciiOn {
			return "[F]"
		}
		return "·"
	}
}

func padRightVisible(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return s + strings.Repeat(" ", width-w)
}

func padLeftVisible(s string, width int) string {
	w := lipgloss.Width(s)
	if w >= width {
		return s
	}
	return strings.Repeat(" ", width-w) + s
}

func sanitizeOneLine(s string) string {
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
