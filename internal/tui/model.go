package tui

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
)

type uiView int

const (
	viewArchive uiView = iota
	viewTransfer
)

type uiMode int

const (
	modeBrowse uiMode = iota
	modeFilter
	modeInputMkdir
	modeInputRename
	modeInputUpload
	modeConfirmDelete
	modeConfirmQuit
	modeHelp
)

const transferTickInterval = 180 * time.Millisecond

type sortPreset struct {
	label      string
	asciiLabel string
	field      string
	order      string
}

var sortPresets = []sortPreset{
	{label: "名称 ↑", asciiLabel: "名称 ^", field: "file_name", order: "asc"},
	{label: "时间 ↓", asciiLabel: "时间 v", field: "updated_at", order: "desc"},
	{label: "大小 ↓", asciiLabel: "大小 v", field: "size", order: "desc"},
}

func sortPresetLabel(idx int, ascii bool) string {
	if idx < 0 || idx >= len(sortPresets) {
		idx = 0
	}
	if ascii {
		return sortPresets[idx].asciiLabel
	}
	return sortPresets[idx].label
}

type dirLoadedMsg struct {
	reqID uint64
	dir   string
	files []quarkpan.FileItem
	err   error
}

type quotaLoadedMsg struct {
	quota *quarkpan.MemberQuota
	err   error
}

type opDoneMsg struct {
	toast       string
	isErr       bool
	reloadDir   bool
	clearSelect bool
	pruneSelect []string
}

type enqueueReadyMsg struct {
	kind      TransferKind
	dlJobs    []DownloadJob
	ulJobs    []UploadJob
	remoteDir string
	err       error
}

type transferTickMsg struct{}

// Model 是 kkpan 日式二次元全屏文件管理器与后台传输中心的 Bubble Tea 状态机
type Model struct {
	backend   CloudBackend
	transfers *TransferManager

	width  int
	height int

	view uiView

	cwd         string
	sortIdx     int
	loading     bool
	dirInFlight bool
	dirReqID    uint64
	allFiles    []quarkpan.FileItem
	visible     []quarkpan.FileItem
	cursor      int
	scrollTop   int
	selected    map[string]quarkpan.FileItem
	lastDirPos  map[string]int

	transferFilter  TransferFilter
	transferCursor  int
	transferScroll  int
	transferTasks   []TransferTask
	transferSummary TransferSummary
	tickActive      bool

	mode             uiMode
	filterQuery      string
	inputValue       string
	pendingSurrogate rune
	renameTarget     quarkpan.FileItem

	nickname   string
	memberType string
	saveDir    string
	quotaUsed  int64
	quotaTotal int64
	hasQuota   bool

	theme        ui.ThemeName
	themeChanged bool

	statusMsg string
	statusErr bool
}

// NewModel 初始化全屏档案浏览器及后台传输调度状态机
func NewModel(backend CloudBackend, startDir string) *Model {
	if strings.TrimSpace(startDir) == "" {
		startDir = "/"
	}
	nick, _, mtype, saveDir, theme, _ := backend.UserSummary()
	if theme == "" {
		theme = ui.ThemeCelestial
	}
	return &Model{
		backend:    backend,
		transfers:  NewTransferManager(backend),
		width:      110,
		height:     30,
		view:       viewArchive,
		cwd:        quarkpan.NormalizePath("/", startDir),
		loading:    true,
		selected:   make(map[string]quarkpan.FileItem),
		lastDirPos: make(map[string]int),
		nickname:   nick,
		memberType: mtype,
		saveDir:    saveDir,
		theme:      theme,
	}
}

// Close 优雅取消所有未完成的后台传输任务
func (m *Model) Close() {
	if m.transfers != nil {
		m.transfers.CancelAll()
	}
}

func (m *Model) Init() tea.Cmd {
	return tea.Batch(m.loadDirCmd(m.cwd, false), m.loadQuotaCmd())
}

func (m *Model) loadDirCmd(dir string, force bool) tea.Cmd {
	m.dirReqID++
	m.dirInFlight = true
	m.loading = true
	reqID := m.dirReqID
	preset := sortPresets[m.sortIdx]
	cleanDir := quarkpan.NormalizePath("/", dir)
	return func() tea.Msg {
		files, err := m.backend.ListDir(cleanDir, &quarkpan.ListOptions{
			SortField:    preset.field,
			SortOrder:    preset.order,
			ForceRefresh: force,
		})
		return dirLoadedMsg{reqID: reqID, dir: cleanDir, files: files, err: err}
	}
}

func (m *Model) loadQuotaCmd() tea.Cmd {
	return func() tea.Msg {
		q, err := m.backend.Quota()
		return quotaLoadedMsg{quota: q, err: err}
	}
}

func (m *Model) scheduleTransferTick() tea.Cmd {
	return tea.Tick(transferTickInterval, func(time.Time) tea.Msg {
		return transferTickMsg{}
	})
}

func (m *Model) ensureTransferTick() tea.Cmd {
	if m.tickActive || m.transfers == nil || !m.transfers.HasActive() {
		return nil
	}
	m.tickActive = true
	return m.scheduleTransferTick()
}

func (m *Model) refreshTransferSnapshot() {
	if m.transfers == nil {
		return
	}
	m.transferTasks, m.transferSummary = m.transfers.Snapshot()
	m.clampTransferCursor()
}

func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 {
			m.width = msg.Width
		}
		if msg.Height > 0 {
			m.height = msg.Height
		}
		m.clampCursor()
		m.clampTransferCursor()
		return m, nil

	case dirLoadedMsg:
		if msg.reqID != m.dirReqID {
			return m, nil
		}
		m.dirInFlight = false
		m.loading = false
		if msg.err != nil {
			m.statusMsg = msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		m.cwd = msg.dir
		m.allFiles = msg.files
		m.applyFilterAndSort()
		if saved, ok := m.lastDirPos[m.cwd]; ok {
			m.cursor = saved
		} else {
			m.cursor = 0
		}
		m.clampCursor()
		return m, nil

	case quotaLoadedMsg:
		if msg.err == nil && msg.quota != nil {
			m.quotaUsed = msg.quota.UseCapacity
			m.quotaTotal = msg.quota.TotalCapacity
			m.hasQuota = msg.quota.TotalCapacity > 0
			if msg.quota.MemberType != "" {
				m.memberType = msg.quota.MemberType
			}
		}
		return m, nil

	case enqueueReadyMsg:
		if msg.err != nil {
			m.statusMsg = msg.err.Error()
			m.statusErr = true
			return m, nil
		}
		if msg.kind == TransferDownload {
			n := m.transfers.EnqueueDownloads(msg.dlJobs)
			m.refreshTransferSnapshot()
			if n == 0 {
				m.statusMsg = "所选目录中没有可下载的文件"
				m.statusErr = false
				return m, nil
			}
			m.statusMsg = fmt.Sprintf("已加入下载队列 (%d 项) · 可继续浏览，按 [Tab] 查看传输中心", n)
			m.statusErr = false
			return m, m.ensureTransferTick()
		}

		n := m.transfers.EnqueueUploads(msg.ulJobs)
		m.refreshTransferSnapshot()
		if n == 0 {
			m.statusMsg = "本地目录内没有可上传的文件"
			m.statusErr = false
			return m, nil
		}
		m.statusMsg = fmt.Sprintf("已加入上传队列 (%d 项) -> %s · 按 [Tab] 查看传输中心", n, msg.remoteDir)
		m.statusErr = false
		return m, m.ensureTransferTick()

	case transferTickMsg:
		m.refreshTransferSnapshot()
		var cmds []tea.Cmd

		// 检查是否有已完成的上传任务落在当前浏览目录下，若有则静默刷新文件列表与容量
		if completedDirs := m.transfers.PopCompletedUploadDirs(); len(completedDirs) > 0 {
			needReload := false
			for _, dir := range completedDirs {
				if quarkpan.NormalizePath("/", dir) == m.cwd || strings.HasPrefix(quarkpan.NormalizePath("/", dir), strings.TrimSuffix(m.cwd, "/")+"/") {
					needReload = true
					break
				}
			}
			if needReload && !m.dirInFlight {
				cmds = append(cmds, m.loadDirCmd(m.cwd, true))
			}
			cmds = append(cmds, m.loadQuotaCmd())
		}

		if m.transfers.HasActive() {
			m.tickActive = true
			cmds = append(cmds, m.scheduleTransferTick())
		} else {
			m.tickActive = false
		}
		return m, tea.Batch(cmds...)

	case opDoneMsg:
		m.loading = false
		if msg.clearSelect {
			m.selected = make(map[string]quarkpan.FileItem)
		} else if len(msg.pruneSelect) > 0 {
			for _, fid := range msg.pruneSelect {
				delete(m.selected, fid)
			}
		}
		if msg.toast != "" {
			m.statusMsg = msg.toast
			m.statusErr = msg.isErr
		}
		if msg.reloadDir {
			return m, tea.Batch(m.loadDirCmd(m.cwd, true), m.loadQuotaCmd())
		}
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if msg.Type == tea.KeyCtrlC {
		m.Close()
		return m, tea.Quit
	}

	switch m.mode {
	case modeHelp:
		switch msg.String() {
		case "?", "esc", "q", "enter", " ":
			m.mode = modeBrowse
		}
		return m, nil

	case modeConfirmQuit:
		switch strings.ToLower(msg.String()) {
		case "y", "enter":
			m.Close()
			return m, tea.Quit
		case "n", "esc", "q":
			m.mode = modeBrowse
			m.statusMsg = "已继续保留后台传输任务"
			m.statusErr = false
		}
		return m, nil

	case modeConfirmDelete:
		switch strings.ToLower(msg.String()) {
		case "y", "enter":
			targets := m.activeTargets()
			m.mode = modeBrowse
			if len(targets) == 0 {
				return m, nil
			}
			fids := make([]string, 0, len(targets))
			for _, t := range targets {
				fids = append(fids, t.FID)
			}
			m.loading = true
			return m, func() tea.Msg {
				if err := m.backend.Remove(fids); err != nil {
					return opDoneMsg{toast: err.Error(), isErr: true}
				}
				return opDoneMsg{
					toast:       fmt.Sprintf("已将 %d 项移入回收站", len(fids)),
					reloadDir:   true,
					pruneSelect: fids,
				}
			}
		case "n", "esc", "q":
			m.mode = modeBrowse
			m.statusMsg = "已取消删除操作"
			m.statusErr = false
		}
		return m, nil

	case modeFilter, modeInputMkdir, modeInputRename, modeInputUpload:
		return m.handleTextInputKey(msg)
	}

	if m.view == viewTransfer {
		return m.handleTransferKey(msg)
	}
	return m.handleArchiveKey(msg)
}

func (m *Model) requestQuit() (tea.Model, tea.Cmd) {
	if m.transfers != nil && m.transfers.HasActive() {
		m.mode = modeConfirmQuit
		return m, nil
	}
	m.Close()
	return m, tea.Quit
}

func (m *Model) handleArchiveKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc":
		return m.requestQuit()
	case "tab", "T":
		m.view = viewTransfer
		m.refreshTransferSnapshot()
		return m, m.ensureTransferTick()
	case "?":
		m.mode = modeHelp
	case "up", "k":
		m.moveCursor(-1)
	case "down", "j":
		m.moveCursor(1)
	case "pgup":
		m.moveCursor(-maxInt(1, m.listViewportRows()-2))
	case "pgdown":
		m.moveCursor(maxInt(1, m.listViewportRows()-2))
	case "g":
		m.cursor = 0
		m.clampCursor()
	case "G":
		m.cursor = len(m.visible) - 1
		m.clampCursor()
	case "enter", "l", "right":
		return m.enterCurrentDir()
	case "backspace", "h", "left":
		return m.goParentDir()
	case " ":
		m.toggleSelectCurrent()
	case "a":
		m.toggleSelectAllVisible()
	case "/":
		m.mode = modeFilter
		m.inputValue = m.filterQuery
		m.pendingSurrogate = 0
	case "s":
		m.sortIdx = (m.sortIdx + 1) % len(sortPresets)
		m.applyFilterAndSort()
		m.statusMsg = "排序已切换为: " + sortPresetLabel(m.sortIdx, ui.ASCIIOnly())
		m.statusErr = false
	case "t":
		m.theme = ui.CycleTheme(m.theme)
		m.themeChanged = true
		p := ui.ThemeByName(m.theme)
		m.statusMsg = fmt.Sprintf("已切换主题: %s (%s)", p.DisplayName, p.Name)
		m.statusErr = false
	case "R":
		m.statusMsg = "正在刷新档案目录..."
		m.statusErr = false
		return m, m.loadDirCmd(m.cwd, true)
	case "n":
		m.mode = modeInputMkdir
		m.inputValue = ""
		m.pendingSurrogate = 0
	case "r":
		cur := m.currentItem()
		if cur == nil {
			return m, nil
		}
		m.renameTarget = *cur
		m.inputValue = cur.FileName
		m.pendingSurrogate = 0
		m.mode = modeInputRename
	case "x", "delete":
		targets := m.activeTargets()
		if len(targets) == 0 {
			m.statusMsg = "请先选中或聚焦需要删除的文件"
			m.statusErr = true
			return m, nil
		}
		m.mode = modeConfirmDelete
	case "d":
		return m.startDownload()
	case "u":
		m.mode = modeInputUpload
		m.inputValue = ""
		m.pendingSurrogate = 0
		m.pendingSurrogate = 0
	}
	return m, nil
}

func (m *Model) handleTransferKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m.requestQuit()
	case "tab", "T", "esc":
		m.view = viewArchive
		return m, nil
	case "?":
		m.mode = modeHelp
	case "t":
		m.theme = ui.CycleTheme(m.theme)
		m.themeChanged = true
		p := ui.ThemeByName(m.theme)
		m.statusMsg = fmt.Sprintf("已切换主题: %s (%s)", p.DisplayName, p.Name)
		m.statusErr = false
	case "1":
		m.transferFilter = FilterAll
		m.transferCursor = 0
		m.clampTransferCursor()
	case "2":
		m.transferFilter = FilterActive
		m.transferCursor = 0
		m.clampTransferCursor()
	case "3":
		m.transferFilter = FilterDone
		m.transferCursor = 0
		m.clampTransferCursor()
	case "4":
		m.transferFilter = FilterFailed
		m.transferCursor = 0
		m.clampTransferCursor()
	case "left", "h":
		m.transferFilter = (m.transferFilter + 3) % 4
		m.transferCursor = 0
		m.clampTransferCursor()
	case "right", "l":
		m.transferFilter = (m.transferFilter + 1) % 4
		m.transferCursor = 0
		m.clampTransferCursor()
	case "up", "k":
		m.moveTransferCursor(-1)
	case "down", "j":
		m.moveTransferCursor(1)
	case "pgup":
		m.moveTransferCursor(-maxInt(1, m.transferViewportRows()-2))
	case "pgdown":
		m.moveTransferCursor(maxInt(1, m.transferViewportRows()-2))
	case "g":
		m.transferCursor = 0
		m.clampTransferCursor()
	case "G":
		m.transferCursor = len(m.filteredTransferTasks()) - 1
		m.clampTransferCursor()
	case "c":
		cur := m.currentTransferTask()
		if cur == nil {
			return m, nil
		}
		if m.transfers.Cancel(cur.ID) {
			m.refreshTransferSnapshot()
			m.statusMsg = "已取消传输任务: " + cur.Name
			m.statusErr = false
		} else {
			m.statusMsg = "该任务已结束，无法取消"
			m.statusErr = true
		}
	case "r":
		cur := m.currentTransferTask()
		if cur == nil {
			return m, nil
		}
		if m.transfers.Retry(cur.ID) {
			m.refreshTransferSnapshot()
			m.statusMsg = "已重新加入传输队列: " + cur.Name
			m.statusErr = false
			return m, m.ensureTransferTick()
		}
		m.statusMsg = "仅失败或已取消的任务支持重试"
		m.statusErr = true
	case "x", "delete":
		cur := m.currentTransferTask()
		if cur == nil {
			return m, nil
		}
		if m.transfers.Remove(cur.ID) {
			m.refreshTransferSnapshot()
			m.statusMsg = "已移除记录: " + cur.Name
			m.statusErr = false
		} else {
			m.statusMsg = "进行中任务不可直接移除，请先按 [c] 取消"
			m.statusErr = true
		}
	case "C":
		removed := m.transfers.ClearFinished()
		m.refreshTransferSnapshot()
		if removed > 0 {
			m.statusMsg = fmt.Sprintf("已清理 %d 条已完成记录", removed)
		} else {
			m.statusMsg = "没有可清理的已完成记录"
		}
		m.statusErr = false
	case "u":
		m.mode = modeInputUpload
		m.inputValue = ""
		m.pendingSurrogate = 0
	case "+", "=":
		return m.adjustDownloadConcurrency(1)
	case "-", "_":
		return m.adjustDownloadConcurrency(-1)
	case "a", "A":
		allMode, newLimit := m.transfers.ToggleDownloadAll()
		m.refreshTransferSnapshot()
		if allMode {
			m.statusMsg = "已开启「全部同时下载」模式，所有排队任务立即并发启动"
		} else {
			m.statusMsg = fmt.Sprintf("已切换为受控并发模式（同时下载 %d 项）", newLimit)
		}
		m.statusErr = false
		return m, m.ensureTransferTick()
	}
	return m, nil
}

func (m *Model) adjustDownloadConcurrency(delta int) (tea.Model, tea.Cmd) {
	cur := m.transfers.DownloadLimit()
	if cur == 0 {
		if delta > 0 {
			m.statusMsg = "当前已是「全部同时下载」模式（按 [a] 可切回受控并发）"
			m.statusErr = false
			return m, nil
		}
		cur = 5
	}
	next := cur + delta
	if next < 1 {
		next = 1
	}
	if next > maxConcurrentLimit {
		next = maxConcurrentLimit
	}
	applied := m.transfers.SetDownloadLimit(next)
	m.backend.SaveDownloadLoad(applied)
	m.refreshTransferSnapshot()
	m.statusMsg = fmt.Sprintf("同时下载任务数已调整为 %d（已保存）", applied)
	m.statusErr = false
	return m, m.ensureTransferTick()
}

func (m *Model) handleTextInputKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		if m.mode == modeFilter {
			m.filterQuery = ""
			m.applyFilterAndSort()
		}
		m.mode = modeBrowse
		m.inputValue = ""
		m.pendingSurrogate = 0
		return m, nil

	case tea.KeyBackspace:
		m.pendingSurrogate = 0
		runes := []rune(m.inputValue)
		if len(runes) > 0 {
			m.inputValue = string(runes[:len(runes)-1])
		}
		if m.mode == modeFilter {
			m.filterQuery = m.inputValue
			m.applyFilterAndSort()
		}
		return m, nil

	case tea.KeyEnter:
		val := strings.TrimSpace(m.inputValue)
		prevMode := m.mode
		m.mode = modeBrowse
		m.inputValue = ""
		m.pendingSurrogate = 0
		switch prevMode {
		case modeFilter:
			m.filterQuery = val
			m.applyFilterAndSort()
			return m, nil
		case modeInputMkdir:
			if val == "" {
				return m, nil
			}
			full := path.Join(m.cwd, val)
			if strings.HasPrefix(val, "/") {
				full = quarkpan.NormalizePath("/", val)
			}
			m.loading = true
			return m, func() tea.Msg {
				if err := m.backend.Mkdir(full); err != nil {
					return opDoneMsg{toast: err.Error(), isErr: true}
				}
				return opDoneMsg{toast: "已新建目录: " + val, reloadDir: true}
			}
		case modeInputRename:
			if val == "" || val == m.renameTarget.FileName {
				return m, nil
			}
			fid := m.renameTarget.FID
			m.loading = true
			return m, func() tea.Msg {
				if err := m.backend.Rename(fid, val); err != nil {
					return opDoneMsg{toast: err.Error(), isErr: true}
				}
				return opDoneMsg{toast: "重命名成功: " + val, reloadDir: true}
			}
		case modeInputUpload:
			cleanLocal := trimMatchedQuotes(val)
			if cleanLocal == "" {
				return m, nil
			}
			destDir := m.cwd
			m.statusMsg = fmt.Sprintf("正在准备上传至 %s...", destDir)
			m.statusErr = false
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				jobs, err := m.backend.PrepareUploads(ctx, []string{cleanLocal}, destDir)
				return enqueueReadyMsg{
					kind:      TransferUpload,
					ulJobs:    jobs,
					remoteDir: destDir,
					err:       err,
				}
			}
		}
		return m, nil

	default:
		switch msg.Type {
		case tea.KeySpace:
			m.pendingSurrogate = 0
			m.inputValue += " "
		case tea.KeyRunes:
			for _, r := range msg.Runes {
				m.appendInputRune(r)
			}
		default:
			return m, nil
		}
		if m.mode == modeFilter {
			m.filterQuery = m.inputValue
			m.applyFilterAndSort()
		}
		return m, nil
	}
}

// appendInputRune 将输入字符追加到输入缓冲，合并 Windows 控制台拆分的 UTF-16 代理对（如 Emoji）
func (m *Model) appendInputRune(r rune) {
	// NOTE: 忽略空字符与控制字符，且保留已挂起的高代理项，避免修饰键空事件打断代理对
	if r == 0 || unicode.IsControl(r) {
		return
	}
	if r >= 0xD800 && r <= 0xDBFF {
		m.pendingSurrogate = r
		return
	}
	if r >= 0xDC00 && r <= 0xDFFF {
		if m.pendingSurrogate != 0 {
			combined := utf16.DecodeRune(m.pendingSurrogate, r)
			m.pendingSurrogate = 0
			if combined != utf8.RuneError && !unicode.IsControl(combined) {
				m.inputValue += string(combined)
			}
		}
		return
	}
	m.pendingSurrogate = 0
	m.inputValue += string(r)
}

func trimMatchedQuotes(raw string) string {
	s := strings.TrimSpace(raw)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return strings.TrimSpace(s[1 : len(s)-1])
		}
	}
	return s
}

func (m *Model) enterCurrentDir() (tea.Model, tea.Cmd) {
	if m.dirInFlight {
		return m, nil
	}
	cur := m.currentItem()
	if cur == nil || !cur.IsDir {
		return m, nil
	}
	m.lastDirPos[m.cwd] = m.cursor
	m.filterQuery = ""
	nextDir := path.Join(m.cwd, cur.FileName)
	return m, m.loadDirCmd(nextDir, false)
}

func (m *Model) goParentDir() (tea.Model, tea.Cmd) {
	if m.dirInFlight || m.cwd == "/" {
		return m, nil
	}
	m.lastDirPos[m.cwd] = m.cursor
	m.filterQuery = ""
	parent := path.Dir(m.cwd)
	if parent == "." || parent == "" {
		parent = "/"
	}
	return m, m.loadDirCmd(parent, false)
}

func (m *Model) startDownload() (tea.Model, tea.Cmd) {
	targets := m.activeTargets()
	if len(targets) == 0 {
		m.statusMsg = "请先选中或聚焦需要下载的文件"
		m.statusErr = true
		return m, nil
	}
	baseCWD := m.cwd
	for i := range targets {
		if targets[i].FullPath == "" {
			targets[i].FullPath = path.Join(baseCWD, targets[i].FileName)
		}
	}
	m.selected = make(map[string]quarkpan.FileItem)
	m.statusMsg = fmt.Sprintf("正在解析下载任务 (%d 项)...", len(targets))
	m.statusErr = false

	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		jobs, err := m.backend.PrepareDownloads(ctx, targets, baseCWD)
		return enqueueReadyMsg{
			kind:   TransferDownload,
			dlJobs: jobs,
			err:    err,
		}
	}
}

func (m *Model) moveCursor(delta int) {
	if len(m.visible) == 0 {
		m.cursor = 0
		m.scrollTop = 0
		return
	}
	m.cursor += delta
	m.clampCursor()
}

func (m *Model) clampCursor() {
	if len(m.visible) == 0 {
		m.cursor = 0
		m.scrollTop = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.visible) {
		m.cursor = len(m.visible) - 1
	}
	vp := m.listViewportRows()
	if vp < 1 {
		vp = 1
	}
	if m.cursor < m.scrollTop {
		m.scrollTop = m.cursor
	}
	if m.cursor >= m.scrollTop+vp {
		m.scrollTop = m.cursor - vp + 1
	}
	if m.scrollTop < 0 {
		m.scrollTop = 0
	}
}

func (m *Model) filteredTransferTasks() []TransferTask {
	if m.transferFilter == FilterAll {
		return m.transferTasks
	}
	out := make([]TransferTask, 0, len(m.transferTasks))
	for _, t := range m.transferTasks {
		switch m.transferFilter {
		case FilterActive:
			if t.Status.IsActive() {
				out = append(out, t)
			}
		case FilterDone:
			if t.Status.IsSuccess() {
				out = append(out, t)
			}
		case FilterFailed:
			if t.Status == StatusFailed || t.Status == StatusCanceled {
				out = append(out, t)
			}
		}
	}
	return out
}

func (m *Model) currentTransferTask() *TransferTask {
	list := m.filteredTransferTasks()
	if m.transferCursor < 0 || m.transferCursor >= len(list) {
		return nil
	}
	t := list[m.transferCursor]
	return &t
}

func (m *Model) moveTransferCursor(delta int) {
	list := m.filteredTransferTasks()
	if len(list) == 0 {
		m.transferCursor = 0
		m.transferScroll = 0
		return
	}
	m.transferCursor += delta
	m.clampTransferCursor()
}

func (m *Model) clampTransferCursor() {
	list := m.filteredTransferTasks()
	if len(list) == 0 {
		m.transferCursor = 0
		m.transferScroll = 0
		return
	}
	if m.transferCursor < 0 {
		m.transferCursor = 0
	}
	if m.transferCursor >= len(list) {
		m.transferCursor = len(list) - 1
	}
	vp := m.transferViewportRows()
	if vp < 1 {
		vp = 1
	}
	if m.transferCursor < m.transferScroll {
		m.transferScroll = m.transferCursor
	}
	if m.transferCursor >= m.transferScroll+vp {
		m.transferScroll = m.transferCursor - vp + 1
	}
	if m.transferScroll < 0 {
		m.transferScroll = 0
	}
}

func (m *Model) currentItem() *quarkpan.FileItem {
	if m.cursor < 0 || m.cursor >= len(m.visible) {
		return nil
	}
	it := m.visible[m.cursor]
	return &it
}

func (m *Model) toggleSelectCurrent() {
	cur := m.currentItem()
	if cur == nil {
		return
	}
	if _, ok := m.selected[cur.FID]; ok {
		delete(m.selected, cur.FID)
	} else {
		m.selected[cur.FID] = *cur
	}
	if m.cursor < len(m.visible)-1 {
		m.moveCursor(1)
	}
}

func (m *Model) toggleSelectAllVisible() {
	if len(m.visible) == 0 {
		return
	}
	allSelected := true
	for _, it := range m.visible {
		if _, ok := m.selected[it.FID]; !ok {
			allSelected = false
			break
		}
	}
	if allSelected {
		for _, it := range m.visible {
			delete(m.selected, it.FID)
		}
		return
	}
	for _, it := range m.visible {
		m.selected[it.FID] = it
	}
}

func (m *Model) activeTargets() []quarkpan.FileItem {
	if len(m.selected) > 0 {
		out := make([]quarkpan.FileItem, 0, len(m.selected))
		for _, v := range m.selected {
			out = append(out, v)
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].FileName < out[j].FileName
		})
		return out
	}
	if cur := m.currentItem(); cur != nil {
		return []quarkpan.FileItem{*cur}
	}
	return nil
}

func (m *Model) applyFilterAndSort() {
	q := strings.ToLower(strings.TrimSpace(m.filterQuery))
	filtered := make([]quarkpan.FileItem, 0, len(m.allFiles))
	for _, it := range m.allFiles {
		if q == "" || strings.Contains(strings.ToLower(it.FileName), q) {
			filtered = append(filtered, it)
		}
	}

	preset := sortPresets[m.sortIdx]
	sort.SliceStable(filtered, func(i, j int) bool {
		if filtered[i].IsDir != filtered[j].IsDir {
			return filtered[i].IsDir
		}
		switch preset.field {
		case "size":
			if filtered[i].Size != filtered[j].Size {
				return filtered[i].Size > filtered[j].Size
			}
		case "updated_at":
			if filtered[i].UpdatedAt != filtered[j].UpdatedAt {
				return filtered[i].UpdatedAt > filtered[j].UpdatedAt
			}
		}
		return strings.ToLower(filtered[i].FileName) < strings.ToLower(filtered[j].FileName)
	})

	m.visible = filtered
	m.clampCursor()
}

func (m *Model) listViewportRows() int {
	// 顶栏 2 行 + 主体框上下边 2 行 + 表头 1 行 + 底栏 3 行
	vp := m.height - 8
	if vp < 4 {
		vp = 4
	}
	return vp
}

func (m *Model) transferViewportRows() int {
	// 传输星港多占 1 行筛选页签栏，其余与档案列表框等高（确保切换视图整体高度不变）
	vp := m.listViewportRows() - 1
	if vp < 3 {
		vp = 3
	}
	return vp
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
