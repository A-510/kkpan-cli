package transfer

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
	"golang.org/x/time/rate"
)

const (
	defaultTorrentMetadataTimeout = 90 * time.Second
	maxRemoteTorrentBytes         = 20 * 1024 * 1024
)

var (
	reMagnetURI     = regexp.MustCompile(`(?i)magnet:\?[^\s"'<>]+`)
	reHexInfoHash   = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)
	reBase32Hash    = regexp.MustCompile(`^[A-Za-z2-7]{32}$`)
	defaultTrackers = []string{
		"udp://tracker.opentrackr.org:1337/announce",
		"udp://open.stealth.si:80/announce",
		"udp://tracker.torrent.eu.org:451/announce",
		"udp://tracker.bittor.pw:1337/announce",
		"https://tracker.tamersunion.org:443/announce",
	}
)

// TorrentSourceKind 标识 BT 任务来源类型
type TorrentSourceKind string

const (
	SourceLocalFile TorrentSourceKind = "file"
	SourceMagnet    TorrentSourceKind = "magnet"
	SourceRemoteURL TorrentSourceKind = "url"
)

// TorrentSource 描述规范化后的 BT 任务来源
type TorrentSource struct {
	Kind       TorrentSourceKind `json:"kind"`
	Raw        string            `json:"raw"`
	Normalized string            `json:"normalized"`
}

// TorrentFileEntry 描述种子或磁力链接内部的单个文件条目
type TorrentFileEntry struct {
	Index       int    `json:"index"`
	Path        string `json:"path"`
	DisplayPath string `json:"display_path"`
	Size        int64  `json:"size"`
}

// TorrentInfo 描述解析后的种子元信息
type TorrentInfo struct {
	InfoHash    string             `json:"info_hash"`
	Name        string             `json:"name"`
	TotalSize   int64              `json:"total_size"`
	IsMultiFile bool               `json:"is_multi_file"`
	Files       []TorrentFileEntry `json:"files"`
	Trackers    []string           `json:"trackers,omitempty"`
}

// TorrentProgress 描述 BT 下载实时进度快照
type TorrentProgress struct {
	Name        string
	Completed   int64
	Total       int64
	BytesPerSec float64
	ActivePeers int
	TotalPeers  int
	Done        bool
}

// TorrentDownloadConfig 控制 BT 元数据解析与下载行为
type TorrentDownloadConfig struct {
	SaveDir           string
	FileIndices       []int
	FilesExpr         string
	MatchExpr         string
	RateLimit         int64
	MetadataTimeout   time.Duration
	ListenPort        int
	ProxyURL          string
	ExtraTrackers     []string
	NoDefaultTrackers bool
	NoDHT             bool
	DisableTrackers   bool
	InitialPeers      []string
	OnMetadata        func(info *TorrentInfo)
	OnSelectFiles     func(info *TorrentInfo) ([]int, error)
	OnProgress        func(snap TorrentProgress)
}

// DownloadedTorrentFile 描述单个已落盘的 BT 文件
type DownloadedTorrentFile struct {
	Index     int    `json:"index"`
	RelPath   string `json:"rel_path"`
	LocalPath string `json:"local_path"`
	Size      int64  `json:"size"`
}

// TorrentDownloadResult 描述整次 BT 下载任务结果
type TorrentDownloadResult struct {
	Info      TorrentInfo             `json:"info"`
	SaveDir   string                  `json:"save_dir"`
	Files     []DownloadedTorrentFile `json:"files"`
	TotalSize int64                   `json:"total_size"`
	Elapsed   time.Duration           `json:"elapsed"`
}

// ParseTorrentSource 识别本地 .torrent 文件、magnet 链接、裸 InfoHash 或远程 .torrent URL
func ParseTorrentSource(raw string) (*TorrentSource, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("请提供 .torrent 种子文件路径、磁力链接或 InfoHash")
	}

	// 1. 优先检查是否为存在的本地文件（避免路径里恰好含类似关键字）
	if info, err := os.Stat(trimmed); err == nil {
		if info.IsDir() {
			return nil, fmt.Errorf("指定路径是目录而非 .torrent 文件: %s", trimmed)
		}
		abs, absErr := filepath.Abs(trimmed)
		if absErr != nil {
			abs = trimmed
		}
		return &TorrentSource{
			Kind:       SourceLocalFile,
			Raw:        raw,
			Normalized: abs,
		}, nil
	}

	// 2. 识别磁力链接（支持从整段分享文案中抽取）
	if m := reMagnetURI.FindString(trimmed); m != "" {
		if _, err := metainfo.ParseMagnetUri(m); err != nil {
			return nil, fmt.Errorf("无效的磁力链接: %w", err)
		}
		return &TorrentSource{
			Kind:       SourceMagnet,
			Raw:        raw,
			Normalized: m,
		}, nil
	}

	// 3. 识别裸 40 位 Hex 或 32 位 Base32 InfoHash（Base32 解码要求大写，Hex 规范为小写）
	if reHexInfoHash.MatchString(trimmed) {
		magnet := "magnet:?xt=urn:btih:" + strings.ToLower(trimmed)
		if _, err := metainfo.ParseMagnetUri(magnet); err != nil {
			return nil, fmt.Errorf("无效的 InfoHash: %w", err)
		}
		return &TorrentSource{
			Kind:       SourceMagnet,
			Raw:        raw,
			Normalized: magnet,
		}, nil
	}
	if reBase32Hash.MatchString(trimmed) {
		magnet := "magnet:?xt=urn:btih:" + strings.ToUpper(trimmed)
		if _, err := metainfo.ParseMagnetUri(magnet); err != nil {
			return nil, fmt.Errorf("无效的 InfoHash: %w", err)
		}
		return &TorrentSource{
			Kind:       SourceMagnet,
			Raw:        raw,
			Normalized: magnet,
		}, nil
	}

	// 4. 识别 HTTP/HTTPS 远程 .torrent 地址
	if u, err := url.Parse(trimmed); err == nil && (strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")) && u.Host != "" {
		return &TorrentSource{
			Kind:       SourceRemoteURL,
			Raw:        raw,
			Normalized: trimmed,
		}, nil
	}

	return nil, fmt.Errorf("未识别的种子路径或磁力链接: %s", trimmed)
}

// ParseFileIndexExpr 解析 1-based 文件序号表达式（如 "1,3,5-7"），返回去重保序列表
func ParseFileIndexExpr(expr string, maxIndex int) ([]int, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return nil, nil
	}
	if maxIndex <= 0 {
		return nil, fmt.Errorf("种子文件列表为空，无法按序号选择")
	}

	seen := make(map[int]bool)
	var out []int
	addIndex := func(idx int) error {
		if idx < 1 || idx > maxIndex {
			return fmt.Errorf("文件序号 %d 超出有效范围 [1-%d]", idx, maxIndex)
		}
		if !seen[idx] {
			seen[idx] = true
			out = append(out, idx)
		}
		return nil
	}

	for _, part := range strings.Split(trimmed, ",") {
		seg := strings.TrimSpace(part)
		if seg == "" {
			continue
		}
		if strings.Contains(seg, "-") {
			bounds := strings.Split(seg, "-")
			if len(bounds) != 2 {
				return nil, fmt.Errorf("无效的序号区间格式: %q", seg)
			}
			start, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
			if err != nil {
				return nil, fmt.Errorf("无效的起始序号 %q: %w", bounds[0], err)
			}
			end, err := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err != nil {
				return nil, fmt.Errorf("无效的结束序号 %q: %w", bounds[1], err)
			}
			if start > end {
				return nil, fmt.Errorf("序号区间起始值不能大于结束值: %q", seg)
			}
			for i := start; i <= end; i++ {
				if err := addIndex(i); err != nil {
					return nil, err
				}
			}
			continue
		}
		idx, err := strconv.Atoi(seg)
		if err != nil {
			return nil, fmt.Errorf("无效的文件序号 %q: %w", seg, err)
		}
		if err := addIndex(idx); err != nil {
			return nil, err
		}
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("未解析到任何有效的文件序号")
	}
	return out, nil
}

// SelectTorrentFiles 根据序号表达式与关键词/通配符筛选目标文件（两者均指定时取交集）
func SelectTorrentFiles(files []TorrentFileEntry, filesExpr, matchExpr string) ([]TorrentFileEntry, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("种子内不包含任何文件")
	}

	candidates := files
	if strings.TrimSpace(filesExpr) != "" {
		indices, err := ParseFileIndexExpr(filesExpr, len(files))
		if err != nil {
			return nil, err
		}
		wanted := make(map[int]bool, len(indices))
		for _, idx := range indices {
			wanted[idx] = true
		}
		filtered := make([]TorrentFileEntry, 0, len(indices))
		for _, f := range candidates {
			if wanted[f.Index] {
				filtered = append(filtered, f)
			}
		}
		candidates = filtered
	}

	if strings.TrimSpace(matchExpr) != "" {
		var patterns []string
		for _, raw := range strings.Split(matchExpr, ",") {
			if p := strings.TrimSpace(raw); p != "" {
				patterns = append(patterns, strings.ToLower(p))
			}
		}
		if len(patterns) > 0 {
			filtered := make([]TorrentFileEntry, 0, len(candidates))
			for _, f := range candidates {
				if matchesAnyTorrentPattern(f, patterns) {
					filtered = append(filtered, f)
				}
			}
			candidates = filtered
		}
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("没有文件符合筛选条件")
	}
	return candidates, nil
}

func matchesAnyTorrentPattern(f TorrentFileEntry, patterns []string) bool {
	relLower := strings.ToLower(f.Path)
	dispLower := strings.ToLower(f.DisplayPath)
	baseLower := strings.ToLower(path.Base(f.Path))

	for _, pat := range patterns {
		hasGlob := strings.ContainsAny(pat, "*?[")
		if hasGlob {
			if ok, err := path.Match(pat, baseLower); err == nil && ok {
				return true
			}
			if ok, err := path.Match(pat, relLower); err == nil && ok {
				return true
			}
			if ok, err := path.Match(pat, dispLower); err == nil && ok {
				return true
			}
			continue
		}
		if strings.Contains(relLower, pat) || strings.Contains(dispLower, pat) {
			return true
		}
	}
	return false
}

// InspectTorrentBytes 从内存中的 .torrent 字节直接解析种子元信息（纯离线）
func InspectTorrentBytes(raw []byte) (*TorrentInfo, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("种子数据为空")
	}
	mi, err := metainfo.Load(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("解析种子元数据失败: %w", err)
	}
	return metaInfoToTorrentInfo(mi)
}

// InspectTorrent 解析本地/远程 .torrent 或磁力链接的文件列表与元信息
func InspectTorrent(ctx context.Context, source string, cfg TorrentDownloadConfig) (*TorrentInfo, error) {
	src, err := ParseTorrentSource(source)
	if err != nil {
		return nil, err
	}

	switch src.Kind {
	case SourceLocalFile:
		mi, err := metainfo.LoadFromFile(src.Normalized)
		if err != nil {
			return nil, fmt.Errorf("读取本地种子文件失败: %w", err)
		}
		return metaInfoToTorrentInfo(mi)
	case SourceRemoteURL:
		raw, err := fetchRemoteTorrentBytes(ctx, src.Normalized, cfg.ProxyURL)
		if err != nil {
			return nil, err
		}
		return InspectTorrentBytes(raw)
	case SourceMagnet:
		tmpDir, err := os.MkdirTemp("", "kkpan-bt-inspect-*")
		if err != nil {
			return nil, fmt.Errorf("创建临时目录失败: %w", err)
		}
		defer os.RemoveAll(tmpDir)

		cl, stor, err := newBTClient(tmpDir, cfg)
		if err != nil {
			return nil, err
		}
		defer func() {
			cl.Close()
			if stor != nil {
				_ = stor.Close()
			}
		}()

		t, err := cl.AddMagnet(src.Normalized)
		if err != nil {
			return nil, fmt.Errorf("添加磁力任务失败: %w", err)
		}
		defer t.Drop()

		attachTrackersAndPeers(t, cfg)
		if err := waitTorrentMetadata(ctx, t, cfg.MetadataTimeout); err != nil {
			return nil, err
		}
		return torrentHandleToInfo(t)
	default:
		return nil, fmt.Errorf("不支持的种子来源类型: %s", src.Kind)
	}
}

// DownloadTorrent 下载本地/远程种子或磁力链接中选定的文件到 SaveDir
func DownloadTorrent(ctx context.Context, source string, rawTorrentBytes []byte, cfg TorrentDownloadConfig) (*TorrentDownloadResult, error) {
	saveDir := strings.TrimSpace(cfg.SaveDir)
	if saveDir == "" {
		return nil, fmt.Errorf("保存目录不能为空")
	}
	absSaveDir, err := filepath.Abs(saveDir)
	if err != nil {
		return nil, fmt.Errorf("解析保存目录失败: %w", err)
	}
	if err := os.MkdirAll(absSaveDir, 0755); err != nil {
		return nil, fmt.Errorf("创建保存目录失败: %w", err)
	}

	var src *TorrentSource
	var mi *metainfo.MetaInfo
	if len(rawTorrentBytes) > 0 {
		loaded, err := metainfo.Load(bytes.NewReader(rawTorrentBytes))
		if err != nil {
			return nil, fmt.Errorf("解析种子字节失败: %w", err)
		}
		mi = loaded
	} else {
		src, err = ParseTorrentSource(source)
		if err != nil {
			return nil, err
		}
		if src.Kind == SourceRemoteURL {
			fetched, err := fetchRemoteTorrentBytes(ctx, src.Normalized, cfg.ProxyURL)
			if err != nil {
				return nil, err
			}
			loaded, err := metainfo.Load(bytes.NewReader(fetched))
			if err != nil {
				return nil, fmt.Errorf("解析远程种子文件失败: %w", err)
			}
			mi = loaded
		} else if src.Kind == SourceLocalFile {
			loaded, err := metainfo.LoadFromFile(src.Normalized)
			if err != nil {
				return nil, fmt.Errorf("读取本地种子文件失败: %w", err)
			}
			mi = loaded
		}
	}

	// 预先在开启客户端之前校验离线种子的路径安全性
	if mi != nil {
		if _, err := metaInfoToTorrentInfo(mi); err != nil {
			return nil, err
		}
	}

	cl, stor, err := newBTClient(absSaveDir, cfg)
	if err != nil {
		return nil, err
	}
	closed := false
	closeAll := func() {
		if closed {
			return
		}
		closed = true
		cl.Close()
		if stor != nil {
			_ = stor.Close()
		}
	}
	defer closeAll()

	var t *torrent.Torrent
	if mi != nil {
		t, err = cl.AddTorrent(mi)
	} else {
		t, err = cl.AddMagnet(src.Normalized)
	}
	if err != nil {
		return nil, fmt.Errorf("创建 BT 下载任务失败: %w", err)
	}

	attachTrackersAndPeers(t, cfg)
	if err := waitTorrentMetadata(ctx, t, cfg.MetadataTimeout); err != nil {
		return nil, err
	}

	info, err := torrentHandleToInfo(t)
	if err != nil {
		return nil, err
	}
	if cfg.OnMetadata != nil {
		cfg.OnMetadata(info)
	}

	selectedEntries, err := resolveSelectedEntries(info, cfg)
	if err != nil {
		return nil, err
	}

	// 记录下载启动前已存在的文件路径，确保只清理本次因跨分片产生的新建未选中残片
	preExisting := snapshotExistingFiles(absSaveDir)

	tFiles := t.Files()
	if len(tFiles) != len(info.Files) {
		return nil, fmt.Errorf("种子文件清单数量异常 (%d != %d)", len(tFiles), len(info.Files))
	}

	selectedIdxSet := make(map[int]bool, len(selectedEntries))
	for _, e := range selectedEntries {
		selectedIdxSet[e.Index] = true
	}

	var selectedHandles []*torrent.File
	var selectedTotalBytes int64
	selectedDiskPaths := make(map[string]bool, len(selectedEntries))
	resultFiles := make([]DownloadedTorrentFile, 0, len(selectedEntries))

	for i, tf := range tFiles {
		idx := i + 1
		diskPath, err := resolveTorrentDiskPath(absSaveDir, tf.Path())
		if err != nil {
			return nil, err
		}
		if !selectedIdxSet[idx] {
			tf.SetPriority(torrent.PiecePriorityNone)
			continue
		}
		tf.Download()
		selectedHandles = append(selectedHandles, tf)
		selectedTotalBytes += tf.Length()
		selectedDiskPaths[normalizePathKey(diskPath)] = true

		relSlash := filepath.ToSlash(strings.TrimPrefix(filepath.Clean(tf.Path()), string(filepath.Separator)))
		resultFiles = append(resultFiles, DownloadedTorrentFile{
			Index:     idx,
			RelPath:   relSlash,
			LocalPath: diskPath,
			Size:      tf.Length(),
		})
	}

	startAt := time.Now()
	if err := waitSelectedFilesComplete(ctx, t, info.Name, selectedHandles, selectedTotalBytes, cfg.OnProgress); err != nil {
		return nil, err
	}
	elapsed := time.Since(startAt)

	// 先关闭Writers与BT客户端，释放 Windows 文件锁与 .part 句柄再做清理或后续读取上传
	_ = t.CloseWriters()
	closeAll()

	cleanupUnselectedArtifacts(absSaveDir, tFiles, selectedIdxSet, preExisting, selectedDiskPaths)

	if cfg.OnProgress != nil {
		cfg.OnProgress(TorrentProgress{
			Name:      info.Name,
			Completed: selectedTotalBytes,
			Total:     selectedTotalBytes,
			Done:      true,
		})
	}

	return &TorrentDownloadResult{
		Info:      *info,
		SaveDir:   absSaveDir,
		Files:     resultFiles,
		TotalSize: selectedTotalBytes,
		Elapsed:   elapsed,
	}, nil
}

func resolveSelectedEntries(info *TorrentInfo, cfg TorrentDownloadConfig) ([]TorrentFileEntry, error) {
	if len(cfg.FileIndices) > 0 {
		parts := make([]string, 0, len(cfg.FileIndices))
		for _, idx := range cfg.FileIndices {
			parts = append(parts, strconv.Itoa(idx))
		}
		return SelectTorrentFiles(info.Files, strings.Join(parts, ","), cfg.MatchExpr)
	}
	if strings.TrimSpace(cfg.FilesExpr) != "" || strings.TrimSpace(cfg.MatchExpr) != "" {
		return SelectTorrentFiles(info.Files, cfg.FilesExpr, cfg.MatchExpr)
	}
	if cfg.OnSelectFiles != nil {
		picked, err := cfg.OnSelectFiles(info)
		if err != nil {
			return nil, err
		}
		if len(picked) > 0 {
			parts := make([]string, 0, len(picked))
			for _, idx := range picked {
				parts = append(parts, strconv.Itoa(idx))
			}
			return SelectTorrentFiles(info.Files, strings.Join(parts, ","), "")
		}
	}
	return SelectTorrentFiles(info.Files, "", "")
}

func newBTClient(baseDir string, cfg TorrentDownloadConfig) (*torrent.Client, storage.ClientImplCloser, error) {
	discardLogger := slog.New(slog.DiscardHandler)
	stor := storage.NewFileOpts(storage.NewFileClientOpts{
		ClientBaseDir:   baseDir,
		PieceCompletion: storage.NewMapPieceCompletion(),
		DisableMmap:     true,
		Logger:          discardLogger,
	})

	cc := torrent.NewDefaultClientConfig()
	cc.DefaultStorage = stor
	cc.DataDir = baseDir
	cc.ListenPort = cfg.ListenPort
	cc.Seed = false
	cc.NoDefaultPortForwarding = true
	cc.Slogger = discardLogger
	cc.NoDHT = cfg.NoDHT
	cc.DisableTrackers = cfg.DisableTrackers

	if cfg.RateLimit > 0 {
		burst := int(cfg.RateLimit)
		const minBurst = 256 * 1024
		if burst < minBurst {
			burst = minBurst
		}
		cc.DownloadRateLimiter = rate.NewLimiter(rate.Limit(cfg.RateLimit), burst)
	}

	if proxyRaw := strings.TrimSpace(cfg.ProxyURL); proxyRaw != "" {
		parsed, err := url.Parse(proxyRaw)
		if err != nil {
			_ = stor.Close()
			return nil, nil, fmt.Errorf("解析 BT 代理地址失败: %w", err)
		}
		cc.HTTPProxy = http.ProxyURL(parsed)
	}

	cl, err := torrent.NewClient(cc)
	if err != nil {
		_ = stor.Close()
		return nil, nil, fmt.Errorf("初始化 BT 客户端失败: %w", err)
	}
	return cl, stor, nil
}

func attachTrackersAndPeers(t *torrent.Torrent, cfg TorrentDownloadConfig) {
	if !cfg.DisableTrackers {
		var tiers [][]string
		if !cfg.NoDefaultTrackers {
			tiers = append(tiers, defaultTrackers)
		}
		var extras []string
		for _, tr := range cfg.ExtraTrackers {
			if s := strings.TrimSpace(tr); s != "" {
				extras = append(extras, s)
			}
		}
		if len(extras) > 0 {
			tiers = append(tiers, extras)
		}
		if len(tiers) > 0 {
			t.AddTrackers(tiers)
		}
	}

	if len(cfg.InitialPeers) > 0 {
		var peers []torrent.PeerInfo
		for _, raw := range cfg.InitialPeers {
			addrStr := strings.TrimSpace(raw)
			if addrStr == "" {
				continue
			}
			tcpAddr, err := net.ResolveTCPAddr("tcp", addrStr)
			if err != nil {
				continue
			}
			peers = append(peers, torrent.PeerInfo{
				Addr:    tcpAddr,
				Trusted: true,
			})
		}
		if len(peers) > 0 {
			t.AddPeers(peers)
		}
	}
}

func waitTorrentMetadata(ctx context.Context, t *torrent.Torrent, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = defaultTorrentMetadataTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-t.GotInfo():
		return nil
	case <-ctx.Done():
		return fmt.Errorf("等待种子元数据已取消: %w", ctx.Err())
	case <-timer.C:
		return fmt.Errorf("等待磁力链接元数据超时 (%s)，可能暂无可用做种节点 (Peer)，可尝试追加 --tracker 或延长 --timeout", timeout)
	}
}

func waitSelectedFilesComplete(
	ctx context.Context,
	t *torrent.Torrent,
	name string,
	selected []*torrent.File,
	totalBytes int64,
	onProgress func(TorrentProgress),
) error {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()

	prevBytes := int64(0)
	prevTime := time.Now()
	var smoothSpeed float64

	for {
		var completed int64
		allDone := true
		for _, f := range selected {
			bc := f.BytesCompleted()
			if bc > f.Length() {
				bc = f.Length()
			}
			completed += bc
			if !isFilePiecesComplete(t, f) {
				allDone = false
			}
		}

		now := time.Now()
		dt := now.Sub(prevTime).Seconds()
		if dt > 0 {
			inst := float64(completed-prevBytes) / dt
			if inst < 0 {
				inst = 0
			}
			if smoothSpeed == 0 {
				smoothSpeed = inst
			} else {
				smoothSpeed = smoothSpeed*0.6 + inst*0.4
			}
			prevBytes = completed
			prevTime = now
		}

		stats := t.Stats()
		if onProgress != nil {
			onProgress(TorrentProgress{
				Name:        name,
				Completed:   completed,
				Total:       totalBytes,
				BytesPerSec: smoothSpeed,
				ActivePeers: stats.ActivePeers,
				TotalPeers:  stats.TotalPeers,
				Done:        false,
			})
		}

		if allDone {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("BT 下载已中断: %w", ctx.Err())
		case <-t.Closed():
			return fmt.Errorf("BT 任务意外关闭")
		case <-ticker.C:
		}
	}
}

func isFilePiecesComplete(t *torrent.Torrent, f *torrent.File) bool {
	if f.Length() == 0 {
		return true
	}
	if f.BytesCompleted() < f.Length() {
		return false
	}
	begin := f.BeginPieceIndex()
	end := f.EndPieceIndex()
	for i := begin; i < end; i++ {
		if !t.PieceState(i).Complete {
			return false
		}
	}
	return true
}

func fetchRemoteTorrentBytes(ctx context.Context, rawURL, proxyURL string) ([]byte, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if strings.TrimSpace(proxyURL) != "" {
		parsed, err := url.Parse(strings.TrimSpace(proxyURL))
		if err != nil {
			return nil, fmt.Errorf("解析代理地址失败: %w", err)
		}
		transport.Proxy = http.ProxyURL(parsed)
	}
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构建种子下载请求失败: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载远程种子失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("下载远程种子返回异常状态码: %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxRemoteTorrentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("读取远程种子响应失败: %w", err)
	}
	if len(data) > maxRemoteTorrentBytes {
		return nil, fmt.Errorf("远程种子文件超过 20MB 限制")
	}
	return data, nil
}

func metaInfoToTorrentInfo(mi *metainfo.MetaInfo) (*TorrentInfo, error) {
	if mi == nil {
		return nil, fmt.Errorf("空的种子元数据")
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return nil, fmt.Errorf("解析种子 Info 字典失败: %w", err)
	}

	rootName := sanitizeRootName(info.BestName(), mi.HashInfoBytes().HexString())
	upFiles := info.UpvertedFiles()
	if len(upFiles) == 0 {
		return nil, fmt.Errorf("种子内未包含任何文件")
	}

	entries := make([]TorrentFileEntry, 0, len(upFiles))
	var total int64
	isMulti := info.IsDir()
	for i, fi := range upFiles {
		parts := fi.BestPath()
		var relPath string
		if len(parts) > 0 {
			if err := validateSafeParts(parts); err != nil {
				return nil, err
			}
			relPath = strings.Join(parts, "/")
		} else {
			if err := validateSafeParts([]string{rootName}); err != nil {
				return nil, err
			}
			relPath = rootName
		}

		dispPath := relPath
		if isMulti {
			dispPath = rootName + "/" + relPath
		}
		entries = append(entries, TorrentFileEntry{
			Index:       i + 1,
			Path:        relPath,
			DisplayPath: dispPath,
			Size:        fi.Length,
		})
		total += fi.Length
	}

	return &TorrentInfo{
		InfoHash:    mi.HashInfoBytes().HexString(),
		Name:        rootName,
		TotalSize:   total,
		IsMultiFile: isMulti,
		Files:       entries,
		Trackers:    flattenAnnounceList(mi.Announce, mi.UpvertedAnnounceList()),
	}, nil
}

func torrentHandleToInfo(t *torrent.Torrent) (*TorrentInfo, error) {
	if t == nil {
		return nil, fmt.Errorf("BT 任务为空")
	}
	rawInfo := t.Info()
	if rawInfo == nil {
		return nil, fmt.Errorf("尚未获取到种子元数据")
	}
	mi := t.Metainfo()
	rootName := sanitizeRootName(rawInfo.BestName(), t.InfoHash().HexString())
	tFiles := t.Files()
	if len(tFiles) == 0 {
		return nil, fmt.Errorf("种子内未包含任何文件")
	}

	entries := make([]TorrentFileEntry, 0, len(tFiles))
	var total int64
	isMulti := rawInfo.IsDir()
	for i, tf := range tFiles {
		fi := tf.FileInfo()
		parts := fi.BestPath()
		var relPath string
		if len(parts) > 0 {
			if err := validateSafeParts(parts); err != nil {
				return nil, err
			}
			relPath = strings.Join(parts, "/")
		} else {
			if err := validateSafeParts([]string{rootName}); err != nil {
				return nil, err
			}
			relPath = rootName
		}

		dispPath := relPath
		if isMulti {
			dispPath = rootName + "/" + relPath
		}
		entries = append(entries, TorrentFileEntry{
			Index:       i + 1,
			Path:        relPath,
			DisplayPath: dispPath,
			Size:        tf.Length(),
		})
		total += tf.Length()
	}

	return &TorrentInfo{
		InfoHash:    t.InfoHash().HexString(),
		Name:        rootName,
		TotalSize:   total,
		IsMultiFile: isMulti,
		Files:       entries,
		Trackers:    flattenAnnounceList(mi.Announce, mi.UpvertedAnnounceList()),
	}, nil
}

func sanitizeRootName(bestName, fallbackHash string) string {
	trimmed := strings.TrimSpace(bestName)
	if trimmed == "" || trimmed == "." || trimmed == metainfo.NoName {
		return fallbackHash
	}
	return trimmed
}

func validateSafeParts(parts []string) error {
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." || strings.ContainsAny(seg, `/\`) {
			return fmt.Errorf("检测到不安全的种子路径片段: %q", seg)
		}
	}
	return nil
}

func resolveTorrentDiskPath(baseDir, torrentRelPath string) (string, error) {
	cleaned := filepath.Clean(filepath.FromSlash(torrentRelPath))
	if filepath.IsAbs(cleaned) || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("检测到越界种子路径: %s", torrentRelPath)
	}
	full := filepath.Join(baseDir, cleaned)
	rel, err := filepath.Rel(baseDir, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("检测到路径逃逸出保存目录: %s", torrentRelPath)
	}
	return full, nil
}

func flattenAnnounceList(single string, tiers metainfo.AnnounceList) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(u string) {
		trimmed := strings.TrimSpace(u)
		if trimmed != "" && !seen[trimmed] {
			seen[trimmed] = true
			out = append(out, trimmed)
		}
	}
	add(single)
	for _, tier := range tiers {
		for _, u := range tier {
			add(u)
		}
	}
	return out
}

func normalizePathKey(p string) string {
	return strings.ToLower(filepath.Clean(p))
}

func snapshotExistingFiles(root string) map[string]bool {
	out := make(map[string]bool)
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if !d.IsDir() {
			out[normalizePathKey(p)] = true
		}
		return nil
	})
	return out
}

func cleanupUnselectedArtifacts(
	saveDir string,
	tFiles []*torrent.File,
	selectedIdxSet map[int]bool,
	preExisting map[string]bool,
	selectedDiskPaths map[string]bool,
) {
	dirsToPrune := make(map[string]struct{})
	for i, tf := range tFiles {
		idx := i + 1
		if selectedIdxSet[idx] {
			continue
		}
		diskPath, err := resolveTorrentDiskPath(saveDir, tf.Path())
		if err != nil {
			continue
		}
		for _, candidate := range []string{diskPath, diskPath + ".part"} {
			key := normalizePathKey(candidate)
			if preExisting[key] || selectedDiskPaths[key] {
				continue
			}
			if _, statErr := os.Stat(candidate); statErr == nil {
				if remErr := os.Remove(candidate); remErr == nil {
					dirsToPrune[filepath.Dir(candidate)] = struct{}{}
				}
			}
		}
	}

	// 从深到浅移除因清理残片而变空的临时子目录（不触碰 saveDir 本身）
	var sortedDirs []string
	baseClean := normalizePathKey(saveDir)
	for d := range dirsToPrune {
		for cur := d; normalizePathKey(cur) != baseClean; cur = filepath.Dir(cur) {
			if _, err := os.Stat(cur); errors.Is(err, os.ErrNotExist) {
				break
			}
			sortedDirs = append(sortedDirs, cur)
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
		}
	}
	sort.Slice(sortedDirs, func(i, j int) bool {
		return len(sortedDirs[i]) > len(sortedDirs[j])
	})
	seenDir := make(map[string]bool, len(sortedDirs))
	for _, d := range sortedDirs {
		key := normalizePathKey(d)
		if seenDir[key] || key == baseClean {
			continue
		}
		seenDir[key] = true
		_ = os.Remove(d)
	}
}
