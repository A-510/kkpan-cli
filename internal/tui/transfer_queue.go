package tui

import (
	"context"
	"errors"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/kkpan-cli/kkpan/internal/transfer"
	"github.com/kkpan-cli/kkpan/internal/ui"
)

const maxConcurrentLimit = 16

// TransferKind 区分传输方向（下载 / 上传）
type TransferKind int

const (
	TransferDownload TransferKind = iota
	TransferUpload
)

// TransferStatus 描述传输任务生命周期状态
type TransferStatus int

const (
	StatusQueued TransferStatus = iota
	StatusRunning
	StatusDone
	StatusRapid
	StatusSkipped
	StatusFailed
	StatusCanceled
)

// IsActive 判断任务是否仍在排队或执行中
func (s TransferStatus) IsActive() bool {
	return s == StatusQueued || s == StatusRunning
}

// IsSuccess 判断任务是否已正常完结（含秒传和跳过）
func (s TransferStatus) IsSuccess() bool {
	return s == StatusDone || s == StatusRapid || s == StatusSkipped
}

// TransferFilter 定义传输星港列表筛选分类
type TransferFilter int

const (
	FilterAll TransferFilter = iota
	FilterActive
	FilterDone
	FilterFailed
)

// TransferTask 为前端渲染提供的不可变传输任务快照
type TransferTask struct {
	ID          int
	Kind        TransferKind
	Name        string
	Source      string
	Target      string
	RemoteDir   string
	Status      TransferStatus
	Completed   int64
	Total       int64
	BytesPerSec float64
	Error       string
	Hint        string
	CreatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
}

// TransferSummary 汇总当前队列中的传输数量、总瞬时吞吐量与并发上限
type TransferSummary struct {
	Total            int
	ActiveDownload   int
	ActiveUpload     int
	Queued           int
	Done             int
	Failed           int
	TotalBytesPerSec float64
	DownloadLimit    int
	UploadLimit      int
}

type managedTask struct {
	snapshot TransferTask
	dlJob    *DownloadJob
	ulJob    *UploadJob
	cancelFn context.CancelFunc
	canceled bool
}

// TransferManager 管理 TUI 后台异步下载/上传队列、动态并发槽位与生命周期取消
type TransferManager struct {
	backend CloudBackend

	mu                 sync.RWMutex
	cond               *sync.Cond
	dlLimit            int // 0 表示不限制并发（全部同时下载）
	dlPrevLimit        int // 记录切到“全部并发”前的数字上限
	dlRunning          int
	ulLimit            int
	ulRunning          int
	nextID             int
	tasks              map[int]*managedTask
	completedUploadDir map[string]struct{}
	closed             bool
}

// NewTransferManager 初始化支持运行时动态调整并发上限的后台传输管理器
func NewTransferManager(backend CloudBackend) *TransferManager {
	dlLoad, ulLoad := 3, 2
	if backend != nil {
		dlLoad, ulLoad = backend.ConcurrencyLimits()
	}
	if dlLoad <= 0 {
		dlLoad = 3
	}
	if ulLoad <= 0 {
		ulLoad = 2
	}
	m := &TransferManager{
		backend:            backend,
		dlLimit:            dlLoad,
		dlPrevLimit:        dlLoad,
		ulLimit:            ulLoad,
		tasks:              make(map[int]*managedTask),
		completedUploadDir: make(map[string]struct{}),
	}
	m.cond = sync.NewCond(&m.mu)
	return m
}

// DownloadLimit 返回当前下载并行任务上限（0 表示不限制/全部同时下载）
func (m *TransferManager) DownloadLimit() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.dlLimit
}

// UploadLimit 返回当前上传并行任务上限
func (m *TransferManager) UploadLimit() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.ulLimit
}

// SetDownloadLimit 运行时动态调整下载并发上限（<=0 表示全部同时下载，>16 钳制为 16），并立即唤醒排队任务
func (m *TransferManager) SetDownloadLimit(limit int) int {
	if limit <= 0 {
		limit = 0
	} else if limit > maxConcurrentLimit {
		limit = maxConcurrentLimit
	}

	m.mu.Lock()
	m.dlLimit = limit
	if limit > 0 {
		m.dlPrevLimit = limit
	}
	m.cond.Broadcast()
	m.mu.Unlock()
	return limit
}

// ToggleDownloadAll 在“全部同时下载”与“受控并发上限”之间切换，返回是否已处于全部并发模式及当前上限
func (m *TransferManager) ToggleDownloadAll() (allMode bool, newLimit int) {
	m.mu.Lock()
	if m.dlLimit != 0 {
		if m.dlLimit > 0 {
			m.dlPrevLimit = m.dlLimit
		}
		m.dlLimit = 0
		allMode = true
		newLimit = 0
	} else {
		restore := m.dlPrevLimit
		if restore <= 0 {
			restore = 3
		}
		m.dlLimit = restore
		allMode = false
		newLimit = restore
	}
	m.cond.Broadcast()
	m.mu.Unlock()
	return allMode, newLimit
}

// EnqueueDownloads 批量提交下载作业到后台队列，返回成功入队数
func (m *TransferManager) EnqueueDownloads(jobs []DownloadJob) int {
	if len(jobs) == 0 {
		return 0
	}
	var toRun []*managedTask
	var ctxs []context.Context

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0
	}
	now := time.Now()
	for _, job := range jobs {
		j := job
		m.nextID++
		id := m.nextID
		ctx, cancel := context.WithCancel(context.Background())
		src := j.RemotePath
		if src == "" {
			src = "fid:" + j.FID
		}
		mt := &managedTask{
			snapshot: TransferTask{
				ID:        id,
				Kind:      TransferDownload,
				Name:      j.FileName,
				Source:    src,
				Target:    j.DestPath,
				RemoteDir: path.Dir(src),
				Status:    StatusQueued,
				Total:     j.Size,
				CreatedAt: now,
			},
			dlJob:    &j,
			cancelFn: cancel,
		}
		m.tasks[id] = mt
		toRun = append(toRun, mt)
		ctxs = append(ctxs, ctx)
	}
	m.mu.Unlock()

	for i, mt := range toRun {
		go m.runTask(ctxs[i], mt)
	}
	return len(toRun)
}

// EnqueueUploads 批量提交上传作业到后台队列，返回成功入队数
func (m *TransferManager) EnqueueUploads(jobs []UploadJob) int {
	if len(jobs) == 0 {
		return 0
	}
	var toRun []*managedTask
	var ctxs []context.Context

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0
	}
	now := time.Now()
	for _, job := range jobs {
		j := job
		m.nextID++
		id := m.nextID
		ctx, cancel := context.WithCancel(context.Background())
		mt := &managedTask{
			snapshot: TransferTask{
				ID:        id,
				Kind:      TransferUpload,
				Name:      j.FileName,
				Source:    j.LocalPath,
				Target:    path.Join(j.RemoteDir, j.FileName),
				RemoteDir: j.RemoteDir,
				Status:    StatusQueued,
				Total:     j.Size,
				CreatedAt: now,
			},
			ulJob:    &j,
			cancelFn: cancel,
		}
		m.tasks[id] = mt
		toRun = append(toRun, mt)
		ctxs = append(ctxs, ctx)
	}
	m.mu.Unlock()

	for i, mt := range toRun {
		go m.runTask(ctxs[i], mt)
	}
	return len(toRun)
}

func (m *TransferManager) runTask(ctx context.Context, mt *managedTask) {
	kind := mt.snapshot.Kind

	m.mu.Lock()
	for !m.closed && !mt.canceled && ctx.Err() == nil {
		if kind == TransferDownload {
			if m.dlLimit == 0 || m.dlRunning < m.dlLimit {
				break
			}
		} else {
			if m.ulLimit == 0 || m.ulRunning < m.ulLimit {
				break
			}
		}
		m.cond.Wait()
	}

	if m.closed || mt.canceled || ctx.Err() != nil {
		mt.snapshot.Status = StatusCanceled
		mt.snapshot.BytesPerSec = 0
		mt.snapshot.FinishedAt = time.Now()
		m.mu.Unlock()
		return
	}

	if kind == TransferDownload {
		m.dlRunning++
	} else {
		m.ulRunning++
	}
	mt.snapshot.Status = StatusRunning
	mt.snapshot.StartedAt = time.Now()
	dlJob := mt.dlJob
	ulJob := mt.ulJob
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		if kind == TransferDownload {
			m.dlRunning--
		} else {
			m.ulRunning--
		}
		m.cond.Broadcast()
		m.mu.Unlock()
	}()

	onProgress := func(p transfer.ProgressSnapshot) {
		m.mu.Lock()
		if mt.snapshot.Status == StatusRunning {
			if p.Total > 0 {
				mt.snapshot.Total = p.Total
			}
			mt.snapshot.Completed = p.Completed
			mt.snapshot.BytesPerSec = p.BytesPerSec
		}
		m.mu.Unlock()
	}

	if kind == TransferDownload && dlJob != nil {
		res, err := m.backend.ExecuteDownload(ctx, *dlJob, onProgress)
		m.finishDownload(ctx, mt, res, err)
		return
	}
	if kind == TransferUpload && ulJob != nil {
		res, err := m.backend.ExecuteUpload(ctx, *ulJob, onProgress)
		m.finishUpload(ctx, mt, ulJob.RemoteDir, res, err)
	}
}

func (m *TransferManager) finishDownload(ctx context.Context, mt *managedTask, res *transfer.DownloadResult, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mt.snapshot.BytesPerSec = 0
	mt.snapshot.FinishedAt = time.Now()
	if err != nil {
		if mt.canceled || errors.Is(err, context.Canceled) || ctx.Err() != nil {
			mt.snapshot.Status = StatusCanceled
			return
		}
		mt.snapshot.Status = StatusFailed
		mt.snapshot.Error = err.Error()
		mt.snapshot.Hint = ui.FriendlyErrorHint(err)
		return
	}
	if res != nil && res.Skipped {
		mt.snapshot.Status = StatusSkipped
		if res.Size > 0 {
			mt.snapshot.Total = res.Size
			mt.snapshot.Completed = res.Size
		}
		return
	}
	mt.snapshot.Status = StatusDone
	if res != nil && res.Size > 0 {
		mt.snapshot.Total = res.Size
		mt.snapshot.Completed = res.Size
	} else if mt.snapshot.Total > 0 {
		mt.snapshot.Completed = mt.snapshot.Total
	}
}

func (m *TransferManager) finishUpload(ctx context.Context, mt *managedTask, remoteDir string, res *transfer.UploadResult, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	mt.snapshot.BytesPerSec = 0
	mt.snapshot.FinishedAt = time.Now()
	if err != nil {
		if mt.canceled || errors.Is(err, context.Canceled) || ctx.Err() != nil {
			mt.snapshot.Status = StatusCanceled
			return
		}
		mt.snapshot.Status = StatusFailed
		mt.snapshot.Error = err.Error()
		mt.snapshot.Hint = ui.FriendlyErrorHint(err)
		return
	}
	if res != nil {
		if res.Size > 0 {
			mt.snapshot.Total = res.Size
			mt.snapshot.Completed = res.Size
		}
		switch {
		case res.Skipped:
			mt.snapshot.Status = StatusSkipped
			return
		case res.Rapid:
			mt.snapshot.Status = StatusRapid
		default:
			mt.snapshot.Status = StatusDone
		}
	} else {
		mt.snapshot.Status = StatusDone
	}
	if remoteDir != "" {
		m.completedUploadDir[remoteDir] = struct{}{}
	}
}

// Snapshot 返回按 ID 倒序排列的任务快照与汇总统计（线程安全只读值拷贝，不跳行）
func (m *TransferManager) Snapshot() ([]TransferTask, TransferSummary) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	tasks := make([]TransferTask, 0, len(m.tasks))
	sum := TransferSummary{
		DownloadLimit: m.dlLimit,
		UploadLimit:   m.ulLimit,
	}
	for _, mt := range m.tasks {
		t := mt.snapshot
		tasks = append(tasks, t)
		sum.Total++
		switch t.Status {
		case StatusQueued:
			sum.Queued++
		case StatusRunning:
			if t.Kind == TransferDownload {
				sum.ActiveDownload++
			} else {
				sum.ActiveUpload++
			}
			sum.TotalBytesPerSec += t.BytesPerSec
		case StatusDone, StatusRapid, StatusSkipped:
			sum.Done++
		case StatusFailed:
			sum.Failed++
		}
	}
	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].ID > tasks[j].ID
	})
	return tasks, sum
}

// HasActive 判断当前是否存在排队中或进行中的任务
func (m *TransferManager) HasActive() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, mt := range m.tasks {
		if mt.snapshot.Status.IsActive() {
			return true
		}
	}
	return false
}

// Cancel 取消指定 ID 的排队中或进行中任务，并唤醒等待协程立即退出
func (m *TransferManager) Cancel(id int) bool {
	m.mu.Lock()
	mt, ok := m.tasks[id]
	if !ok || !mt.snapshot.Status.IsActive() {
		m.mu.Unlock()
		return false
	}
	mt.canceled = true
	cancel := mt.cancelFn
	if mt.snapshot.Status == StatusQueued {
		mt.snapshot.Status = StatusCanceled
		mt.snapshot.FinishedAt = time.Now()
	}
	m.cond.Broadcast()
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	return true
}

// Retry 重试指定 ID 的失败或已取消任务
func (m *TransferManager) Retry(id int) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	mt, ok := m.tasks[id]
	if !ok || (mt.snapshot.Status != StatusFailed && mt.snapshot.Status != StatusCanceled) {
		m.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	mt.canceled = false
	mt.cancelFn = cancel
	mt.snapshot.Status = StatusQueued
	mt.snapshot.Completed = 0
	mt.snapshot.BytesPerSec = 0
	mt.snapshot.Error = ""
	mt.snapshot.Hint = ""
	mt.snapshot.StartedAt = time.Time{}
	mt.snapshot.FinishedAt = time.Time{}
	m.mu.Unlock()

	go m.runTask(ctx, mt)
	return true
}

// Remove 从列表移除单条已结束（非活跃）的任务记录
func (m *TransferManager) Remove(id int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	mt, ok := m.tasks[id]
	if !ok || mt.snapshot.Status.IsActive() {
		return false
	}
	delete(m.tasks, id)
	return true
}

// ClearFinished 批量清理全部已完成、秒传或跳过的任务记录，返回清理条数
func (m *TransferManager) ClearFinished() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	removed := 0
	for id, mt := range m.tasks {
		if mt.snapshot.Status.IsSuccess() {
			delete(m.tasks, id)
			removed++
		}
	}
	return removed
}

// PopCompletedUploadDirs 取出并清空自上次查询以来新完成的上传目标目录
func (m *TransferManager) PopCompletedUploadDirs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.completedUploadDir) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.completedUploadDir))
	for dir := range m.completedUploadDir {
		out = append(out, dir)
	}
	m.completedUploadDir = make(map[string]struct{})
	return out
}

// CancelAll 优雅停止并取消所有进行中及排队中的后台任务，并唤醒全部等待协程
func (m *TransferManager) CancelAll() {
	m.mu.Lock()
	m.closed = true
	cancels := make([]context.CancelFunc, 0, len(m.tasks))
	now := time.Now()
	for _, mt := range m.tasks {
		if mt.snapshot.Status.IsActive() {
			mt.canceled = true
			if mt.snapshot.Status == StatusQueued {
				mt.snapshot.Status = StatusCanceled
				mt.snapshot.FinishedAt = now
			}
			if mt.cancelFn != nil {
				cancels = append(cancels, mt.cancelFn)
			}
		}
	}
	m.cond.Broadcast()
	m.mu.Unlock()

	for _, fn := range cancels {
		fn()
	}
}
