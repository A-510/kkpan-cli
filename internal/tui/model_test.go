package tui

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kkpan-cli/kkpan/internal/transfer"
	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
)

type mockBackend struct {
	mu          sync.Mutex
	dirs        map[string][]quarkpan.FileItem
	themeSaved  ui.ThemeName
	workdir     string
	renamedTo   string
	removedFIDs []string
	createdDir  string

	dlConcurrency     int
	ulConcurrency     int
	savedDownloadLoad int
	onExecDownload    func(ctx context.Context, job DownloadJob, onProgress func(transfer.ProgressSnapshot)) (*transfer.DownloadResult, error)
	onExecUpload      func(ctx context.Context, job UploadJob, onProgress func(transfer.ProgressSnapshot)) (*transfer.UploadResult, error)
}

func (m *mockBackend) ListDir(dir string, opt *quarkpan.ListOptions) ([]quarkpan.FileItem, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]quarkpan.FileItem(nil), m.dirs[dir]...), nil
}

func (m *mockBackend) Mkdir(fullPath string) error {
	m.mu.Lock()
	m.createdDir = fullPath
	m.mu.Unlock()
	return nil
}

func (m *mockBackend) Rename(fid, newName string) error {
	m.mu.Lock()
	m.renamedTo = newName
	m.mu.Unlock()
	return nil
}

func (m *mockBackend) Remove(fids []string) error {
	m.mu.Lock()
	m.removedFIDs = append([]string(nil), fids...)
	m.mu.Unlock()
	return nil
}

func (m *mockBackend) Quota() (*quarkpan.MemberQuota, error) {
	return &quarkpan.MemberQuota{
		UseCapacity:   1024 * 1024 * 1024,
		TotalCapacity: 6 * 1024 * 1024 * 1024,
		MemberType:    "SVIP",
		IsVIP:         true,
		IsSVIP:        true,
	}, nil
}

func (m *mockBackend) ConcurrencyLimits() (int, int) {
	dl := m.dlConcurrency
	if dl <= 0 {
		dl = 2
	}
	ul := m.ulConcurrency
	if ul <= 0 {
		ul = 2
	}
	return dl, ul
}

func (m *mockBackend) PrepareDownloads(ctx context.Context, targets []quarkpan.FileItem, baseCWD string) ([]DownloadJob, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var jobs []DownloadJob
	var walk func(it quarkpan.FileItem, localBase string)
	walk = func(it quarkpan.FileItem, localBase string) {
		if !it.IsDir {
			rp := it.FullPath
			if rp == "" {
				rp = path.Join(baseCWD, it.FileName)
			}
			jobs = append(jobs, DownloadJob{
				FID:        it.FID,
				FileName:   it.FileName,
				Size:       it.Size,
				RemotePath: rp,
				DestPath:   filepath.Join(localBase, it.FileName),
			})
			return
		}
		dirPath := it.FullPath
		if dirPath == "" {
			dirPath = path.Join(baseCWD, it.FileName)
		}
		for _, ch := range m.dirs[dirPath] {
			ch.FullPath = path.Join(dirPath, ch.FileName)
			walk(ch, filepath.Join(localBase, it.FileName))
		}
	}
	for _, t := range targets {
		walk(t, "D:/Downloads")
	}
	return jobs, nil
}

func (m *mockBackend) ExecuteDownload(ctx context.Context, job DownloadJob, onProgress func(transfer.ProgressSnapshot)) (*transfer.DownloadResult, error) {
	if m.onExecDownload != nil {
		return m.onExecDownload(ctx, job, onProgress)
	}
	if onProgress != nil {
		onProgress(transfer.ProgressSnapshot{Name: job.FileName, Completed: job.Size, Total: job.Size, BytesPerSec: 1024, Done: true})
	}
	return &transfer.DownloadResult{LocalPath: job.DestPath, Size: job.Size}, nil
}

func (m *mockBackend) PrepareUploads(ctx context.Context, localPaths []string, remoteDir string) ([]UploadJob, error) {
	jobs := make([]UploadJob, 0, len(localPaths))
	for _, p := range localPaths {
		stat, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, UploadJob{
			LocalPath: p,
			FileName:  stat.Name(),
			Size:      stat.Size(),
			RemoteDir: remoteDir,
		})
	}
	return jobs, nil
}

func (m *mockBackend) ExecuteUpload(ctx context.Context, job UploadJob, onProgress func(transfer.ProgressSnapshot)) (*transfer.UploadResult, error) {
	if m.onExecUpload != nil {
		return m.onExecUpload(ctx, job, onProgress)
	}
	if onProgress != nil {
		onProgress(transfer.ProgressSnapshot{Name: job.FileName, Completed: job.Size, Total: job.Size, BytesPerSec: 2048, Done: true})
	}
	return &transfer.UploadResult{RemoteName: job.FileName, FID: "u-fid-1", Size: job.Size}, nil
}

func (m *mockBackend) SaveDownloadLoad(load int) {
	m.mu.Lock()
	m.savedDownloadLoad = load
	m.mu.Unlock()
}
func (m *mockBackend) SaveWorkdir(dir string)       { m.workdir = dir }
func (m *mockBackend) SaveTheme(theme ui.ThemeName) { m.themeSaved = theme }
func (m *mockBackend) UserSummary() (string, string, string, string, ui.ThemeName, bool) {
	return "绫波零", "u001", "SVIP", "D:/Downloads", ui.ThemeCelestial, true
}

func newTestModel() (*Model, *mockBackend) {
	mb := &mockBackend{
		dirs: map[string][]quarkpan.FileItem{
			"/": {
				{FID: "d1", FileName: "新番动画", IsDir: true, UpdatedAt: 1700000000},
				{FID: "f1", FileName: "君の名は_BD.mkv", Size: 2048000, MD5: "deadbeef", UpdatedAt: 1700000100},
				{FID: "f2", FileName: "OST.flac", Size: 512000, UpdatedAt: 1700000200},
			},
			"/新番动画": {
				{FID: "f3", FileName: "EP01.mp4", Size: 1024000, UpdatedAt: 1700000300},
			},
		},
	}
	m := NewModel(mb, "/")
	// 手动注入初次加载消息
	m.Update(dirLoadedMsg{reqID: 0, dir: "/", files: mb.dirs["/"]})
	m.Update(quotaLoadedMsg{quota: &quarkpan.MemberQuota{UseCapacity: 1024, TotalCapacity: 4096, MemberType: "SVIP"}})
	return m, mb
}

func waitUntil(t *testing.T, timeout time.Duration, fn func() bool, desc string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("等待超时: %s", desc)
}

func TestTUIThemeCycleAndNavigation(t *testing.T) {
	m, _ := newTestModel()
	defer m.Close()
	if m.theme != ui.ThemeCelestial {
		t.Fatalf("默认主题应为 celestial，实际: %s", m.theme)
	}

	// 按 t 依次切换三套日式二次元主题
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if m.theme != ui.ThemeDossier {
		t.Fatalf("期望切换为 dossier，实际: %s", m.theme)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'t'}})
	if m.theme != ui.ThemeShinbo {
		t.Fatalf("期望切换为 shinbo，实际: %s", m.theme)
	}

	// 进入目录 /新番动画
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("进入目录应触发加载命令")
	}
	m.Update(cmd())
	if m.cwd != "/新番动画" || len(m.visible) != 1 || m.visible[0].FileName != "EP01.mp4" {
		t.Fatalf("进入目录后状态异常: cwd=%s visible=%+v", m.cwd, m.visible)
	}

	// 返回上级
	_, cmd2 := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'h'}})
	m.Update(cmd2())
	if m.cwd != "/" {
		t.Fatalf("返回上级失败: cwd=%s", m.cwd)
	}
}

func TestTUIFilterMultiSelectAndCRUD(t *testing.T) {
	m, mb := newTestModel()
	defer m.Close()

	// 多选第一、二项
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	m.Update(tea.KeyMsg{Type: tea.KeySpace})
	if len(m.selected) != 2 {
		t.Fatalf("期望已选 2 项，实际: %d", len(m.selected))
	}

	// 确认删除流程
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if m.mode != modeConfirmDelete {
		t.Fatalf("期望进入删除确认模式")
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	m.Update(cmd())
	if len(mb.removedFIDs) != 2 {
		t.Fatalf("期望删除 2 项，实际: %+v", mb.removedFIDs)
	}

	// 新建目录输入
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	for _, r := range "剧场版" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, mkCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(mkCmd())
	if mb.createdDir != "/剧场版" {
		t.Fatalf("期望创建目录 /剧场版，实际: %s", mb.createdDir)
	}
}

func TestTUIWideNarrowAndASCIIRendering(t *testing.T) {
	m, _ := newTestModel()
	defer m.Close()

	// 宽屏 120x34：应同时包含左栏 ARCHIVE 与右栏 INSPECTOR
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 34})
	wide := m.View()
	if !strings.Contains(wide, "ARCHIVE //") || !strings.Contains(wide, "INSPECTOR //") || !strings.Contains(wide, "KK-PAN // 星海档案") {
		t.Fatalf("宽屏双栏图鉴元素缺失:\n%s", wide)
	}

	// 窄屏 76x22：自动隐藏 INSPECTOR 右栏
	m.Update(tea.WindowSizeMsg{Width: 76, Height: 22})
	narrow := m.View()
	if strings.Contains(narrow, "INSPECTOR //") {
		t.Fatalf("76 列窄屏应自动折叠 INSPECTOR 栏:\n%s", narrow)
	}

	// 切换到传输星港视图检验窄屏与 ASCII 渲染
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	txNarrow := m.View()
	if !strings.Contains(txNarrow, "TRANSFER //") {
		t.Fatalf("传输界面标题缺失:\n%s", txNarrow)
	}

	// ASCII 降级模式
	ui.SetASCIIOnly(true)
	defer ui.SetASCIIOnly(false)
	m.Update(tea.WindowSizeMsg{Width: 110, Height: 28})
	asciiView := m.View()
	for _, bad := range []string{"╭", "╰", "✦", "❖", "▎", "━", "◈", "↓", "↑"} {
		if strings.Contains(asciiView, bad) {
			t.Fatalf("ASCII 模式不应出现字符 %q:\n%s", bad, asciiView)
		}
	}
	_ = fmt.Sprintf("checked wide=%d narrow=%d", len(wide), len(narrow))
}

func TestTUIBackgroundDownloadWhileBrowsingOtherDirs(t *testing.T) {
	m, mb := newTestModel()
	defer m.Close()

	releaseFirst := make(chan struct{})
	firstStarted := make(chan struct{})
	var count atomic.Int32

	mb.onExecDownload = func(ctx context.Context, job DownloadJob, onProgress func(transfer.ProgressSnapshot)) (*transfer.DownloadResult, error) {
		idx := count.Add(1)
		if onProgress != nil {
			onProgress(transfer.ProgressSnapshot{Name: job.FileName, Completed: job.Size / 2, Total: job.Size, BytesPerSec: 1024 * 1024})
		}
		if idx == 1 {
			close(firstStarted)
			select {
			case <-releaseFirst:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &transfer.DownloadResult{LocalPath: job.DestPath, Size: job.Size}, nil
	}

	// 1. 在根目录移动光标到第二个条目 (OST.flac) 按 d 加入后台下载
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, dlCmd1 := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	if dlCmd1 == nil {
		t.Fatal("期望返回异步准备下载命令")
	}
	m.Update(dlCmd1())
	<-firstStarted

	// 2. 第一个文件仍在下载时，立刻切入 /新番动画 并按 d 下载 EP01.mp4
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	_, enterCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(enterCmd())
	if m.cwd != "/新番动画" {
		t.Fatalf("后台下载期间应允许自由切换目录，实际 cwd=%s", m.cwd)
	}
	_, dlCmd2 := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m.Update(dlCmd2())

	// 3. 切入传输星港视图检查两个任务都在队列中且互不阻塞
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.view != viewTransfer {
		t.Fatalf("按 Tab 应切换到传输星港视图")
	}
	rendered := m.View()
	if !strings.Contains(rendered, "TRANSFER // 传输星港") || !strings.Contains(rendered, "EP01.mp4") || !strings.Contains(rendered, "OST.flac") {
		t.Fatalf("传输界面未同时展示来自不同目录的两个下载任务:\n%s", rendered)
	}

	close(releaseFirst)
	waitUntil(t, 2*time.Second, func() bool {
		return !m.transfers.HasActive()
	}, "全部后台下载任务完成")

	m.Update(transferTickMsg{})
	if m.transferSummary.Done != 2 {
		t.Fatalf("期望 2 个任务全部完成，实际 summary=%+v", m.transferSummary)
	}
}

func TestTUITransferCancelRetryClearAndQuitConfirm(t *testing.T) {
	m, mb := newTestModel()
	defer m.Close()

	var attempt atomic.Int32
	started := make(chan struct{}, 2)

	mb.onExecDownload = func(ctx context.Context, job DownloadJob, onProgress func(transfer.ProgressSnapshot)) (*transfer.DownloadResult, error) {
		n := attempt.Add(1)
		started <- struct{}{}
		if n == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return &transfer.DownloadResult{LocalPath: job.DestPath, Size: job.Size}, nil
	}

	// 将 OST.flac 加入后台下载（索引 2）
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	_, dlCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m.Update(dlCmd())
	<-started

	// 有进行中任务时按 q 应弹出退出确认，按 n 可继续后台传输
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if m.mode != modeConfirmQuit {
		t.Fatalf("有进行中任务按 q 期望进入 modeConfirmQuit，实际 mode=%v", m.mode)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.mode != modeBrowse {
		t.Fatalf("按 n 应取消退出并继续传输")
	}

	// 切到传输页面按 c 取消任务
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	waitUntil(t, 2*time.Second, func() bool {
		tasks, _ := m.transfers.Snapshot()
		return len(tasks) == 1 && tasks[0].Status == StatusCanceled
	}, "任务被取消为 StatusCanceled")

	// 按 4 筛选失败/取消，再按 r 重试，应重新运行并成功完成
	m.Update(transferTickMsg{})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	<-started
	waitUntil(t, 2*time.Second, func() bool {
		tasks, _ := m.transfers.Snapshot()
		return len(tasks) == 1 && tasks[0].Status == StatusDone
	}, "重试任务完成为 StatusDone")

	// 切回 1 全部并按 C 清理已完成记录
	m.Update(transferTickMsg{})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'1'}})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'C'}})
	if len(m.transferTasks) != 0 {
		t.Fatalf("按 C 清理已完成记录后列表应为空，实际: %+v", m.transferTasks)
	}
}

func TestTUIUploadQueueAndAutoReload(t *testing.T) {
	m, mb := newTestModel()
	defer m.Close()

	tmpFile := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(tmpFile, []byte("hello quark"), 0600); err != nil {
		t.Fatal(err)
	}

	// 在根目录按 u 输入带双引号的路径（模拟 Windows 复制文件路径场景）
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	for _, r := range `"` + tmpFile + `"` {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	_, ulCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if ulCmd == nil {
		t.Fatal("上传应返回异步准备命令")
	}
	m.Update(ulCmd())

	waitUntil(t, 2*time.Second, func() bool {
		return !m.transfers.HasActive()
	}, "后台上传完成")

	// 模拟目录新增该文件并触发 transferTickMsg 自动刷新当前目录
	mb.mu.Lock()
	mb.dirs["/"] = append(mb.dirs["/"], quarkpan.FileItem{FID: "u-fid-1", FileName: "notes.txt", Size: 11})
	mb.mu.Unlock()

	_, tickCmd := m.Update(transferTickMsg{})
	if tickCmd == nil {
		t.Fatal("上传到当前目录完成后 tick 应触发目录刷新命令")
	}
	if m.transferSummary.Done != 1 {
		t.Fatalf("期望 1 条已完成上传记录，实际: %+v", m.transferSummary)
	}
}

func TestTUIMultiConcurrentDownloadAndDynamicLimit(t *testing.T) {
	mb := &mockBackend{
		dlConcurrency: 1,
		dirs: map[string][]quarkpan.FileItem{
			"/": {
				{FID: "f1", FileName: "alpha.bin", Size: 1000},
				{FID: "f2", FileName: "beta.bin", Size: 2000},
				{FID: "f3", FileName: "gamma.bin", Size: 3000},
			},
		},
	}

	release := make(chan struct{})
	runningCh := make(chan string, 3)

	mb.onExecDownload = func(ctx context.Context, job DownloadJob, onProgress func(transfer.ProgressSnapshot)) (*transfer.DownloadResult, error) {
		runningCh <- job.FileName
		select {
		case <-release:
			return &transfer.DownloadResult{LocalPath: job.DestPath, Size: job.Size}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	m := NewModel(mb, "/")
	defer m.Close()
	m.Update(dirLoadedMsg{reqID: 0, dir: "/", files: mb.dirs["/"]})

	// 一次性全选 3 个文件并按 d 加入下载队列
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	_, dlCmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	m.Update(dlCmd())

	// 初始并发上限为 1：应有 1 个运行中、2 个排队中
	<-runningCh
	waitUntil(t, 2*time.Second, func() bool {
		_, sum := m.transfers.Snapshot()
		return sum.ActiveDownload == 1 && sum.Queued == 2
	}, "初始并发受限应为 ActiveDownload=1, Queued=2")

	// 切换到传输星港视图后按 '+' 调大并发至 2，应立即唤醒第 2 个排队任务同时下载
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'+'}})
	<-runningCh
	waitUntil(t, 2*time.Second, func() bool {
		_, sum := m.transfers.Snapshot()
		return sum.ActiveDownload == 2 && sum.Queued == 1
	}, "按 + 调高并发后应为 ActiveDownload=2, Queued=1")

	mb.mu.Lock()
	savedLoad := mb.savedDownloadLoad
	mb.mu.Unlock()
	if savedLoad != 2 {
		t.Fatalf("期望保存新并发上限 2，实际 savedDownloadLoad=%d", savedLoad)
	}

	// 按 'a' 一键开启「全部同时下载」，应立即唤醒第 3 个排队任务，实现 3 个任务并发下载
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	<-runningCh
	waitUntil(t, 2*time.Second, func() bool {
		_, sum := m.transfers.Snapshot()
		return sum.ActiveDownload == 3 && sum.Queued == 0 && sum.DownloadLimit == 0
	}, "开启全部同时下载后应 3 个任务全部并发执行")

	m.Update(transferTickMsg{})
	viewOut := m.View()
	if !strings.Contains(viewOut, "并行:全部") {
		t.Fatalf("传输界面未显示 '并行:全部':\n%s", viewOut)
	}

	// 放行全部任务，等待全部完成
	close(release)
	waitUntil(t, 2*time.Second, func() bool {
		return !m.transfers.HasActive()
	}, "全部 3 个并发下载任务完成")
	m.Update(transferTickMsg{})
	if m.transferSummary.Done != 3 {
		t.Fatalf("期望 3 个任务全部完成，实际 summary=%+v", m.transferSummary)
	}
}

func TestTUIUploadPathWithWindowsUTF16SurrogateEmoji(t *testing.T) {
	const emojiName = "1_猫猫快变啊！给我变！ 🐱.mp4"

	t.Run("split_utf16_surrogate_upload_from_transfer_view", func(t *testing.T) {
		m, _ := newTestModel()
		defer m.Close()

		tmpDir := t.TempDir()
		tmpFile := filepath.Join(tmpDir, emojiName)
		if err := os.WriteFile(tmpFile, []byte("emoji-video"), 0600); err != nil {
			t.Fatal(err)
		}

		// 模拟上传完成后在传输星港页再次按 u 输入本地路径
		m.Update(tea.KeyMsg{Type: tea.KeyTab})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
		if m.mode != modeInputUpload {
			t.Fatalf("期望进入 modeInputUpload，实际 mode=%v", m.mode)
		}

		// 模拟 Windows coninput + bubbletea 将非 BMP Emoji 拆为两个 UTF-16 代理项 KeyRunes 事件
		for _, unit := range utf16.Encode([]rune(`"` + tmpFile + `"`)) {
			m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{rune(unit)}})
		}

		_, ulCmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if ulCmd == nil {
			t.Fatal("按 Enter 后应返回上传准备命令")
		}
		m.Update(ulCmd())
		if m.statusErr {
			t.Fatalf("预期路径解析成功，实际报错: %s", m.statusMsg)
		}

		waitUntil(t, 2*time.Second, func() bool {
			return !m.transfers.HasActive()
		}, "含 Emoji 路径的后台上传完成")

		tasks, sum := m.transfers.Snapshot()
		if sum.Done != 1 || len(tasks) != 1 {
			t.Fatalf("期望 1 条已完成任务，实际 sum=%+v tasks=%+v", sum, tasks)
		}
		if tasks[0].Source != tmpFile || tasks[0].Name != emojiName {
			t.Fatalf("任务路径或名称不匹配: got source=%q name=%q want source=%q name=%q", tasks[0].Source, tasks[0].Name, tmpFile, emojiName)
		}
	})

	t.Run("real_quark_backend_prepare_uploads_with_emoji", func(t *testing.T) {
		tmpFile := filepath.Join(t.TempDir(), emojiName)
		if err := os.WriteFile(tmpFile, []byte("abc"), 0600); err != nil {
			t.Fatal(err)
		}
		b := NewQuarkBackend()
		jobs, err := b.PrepareUploads(context.Background(), []string{tmpFile}, "/")
		if err != nil {
			t.Fatalf("PrepareUploads 失败: %v", err)
		}
		if len(jobs) != 1 || jobs[0].LocalPath != tmpFile || jobs[0].FileName != emojiName {
			t.Fatalf("PrepareUploads 返回异常: %+v", jobs)
		}
	})

	t.Run("edge_cases_surrogates_and_keyspace", func(t *testing.T) {
		m, _ := newTestModel()
		defer m.Close()

		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
		// 1. 同一个 KeyMsg 携带完整代理对
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0xD83D, 0xDC31}})
		// 2. 高低代理之间穿插 r == 0 空事件，应依然能正确合并
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0xD83D}})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0}})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0xDC31}})
		// 3. 孤立低代理不产生 U+FFFD，孤立高代理后接普通字符不产生 U+FFFD
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0xDC31}})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{0xD83D}})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
		// 4. KeySpace 携带 Runes: [' '] 时只追加一个空格
		m.Update(tea.KeyMsg{Type: tea.KeySpace, Runes: []rune{' '}})
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})

		want := "🐱🐱a b"
		if m.inputValue != want {
			t.Fatalf("输入缓冲边界用例不匹配: got=%q (% x), want=%q (% x)", m.inputValue, m.inputValue, want, want)
		}
	})
}
