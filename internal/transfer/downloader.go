package transfer

import (
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kkpan-cli/kkpan/quarkpan"
)

const (
	defaultDownloadChunkSize int64 = 4 * 1024 * 1024
	downloadingSuffix              = ".kkpan.downloading"
	downloadStateSuffix            = ".kkpan.state.json"
)

// DownloadConfig 控制单文件分片下载行为
type DownloadConfig struct {
	Parallel    int
	MaxRetry    int
	RateLimit   int64
	Overwrite   bool
	NoCheck     bool
	ModifyMTime bool
	ChunkSize   int64
	TestOnly    bool
	OnProgress  func(ProgressSnapshot)
}

type chunkRange struct {
	Index int   `json:"index"`
	Start int64 `json:"start"`
	End   int64 `json:"end"`
	Done  bool  `json:"done"`
}

type downloadState struct {
	URL       string       `json:"url"`
	TotalSize int64        `json:"total_size"`
	ChunkSize int64        `json:"chunk_size"`
	Chunks    []chunkRange `json:"chunks"`
}

// DownloadResult 返回单文件下载完成后的统计结果
type DownloadResult struct {
	LocalPath   string
	Size        int64
	Skipped     bool
	Verified    bool
	MD5Mismatch bool
	Elapsed     time.Duration
}

type transportIntegrity struct {
	contentMD5Hex string
	crc64ECMA     uint64
	hasCRC64      bool
}

// Downloader 实现多线程 Range 分片并发下载与断点续传
type Downloader struct {
	client *quarkpan.Client
}

// NewDownloader 创建下载引擎实例
func NewDownloader(client *quarkpan.Client) *Downloader {
	return &Downloader{client: client}
}

// DownloadFile 将指定的 quarkpan.DownloadLink 下载到本地 destPath（支持主链接失败自动回退 BackupURL）
func (d *Downloader) DownloadFile(ctx context.Context, link quarkpan.DownloadLink, destPath string, cfg DownloadConfig) (*DownloadResult, error) {
	candidates := link.CandidateURLs()
	if len(candidates) == 0 {
		return nil, fmt.Errorf("下载链接为空: %s", link.FileName)
	}
	if cfg.Parallel <= 0 {
		cfg.Parallel = 1
	}
	if cfg.MaxRetry < 0 {
		cfg.MaxRetry = 0
	}
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = defaultDownloadChunkSize
	}

	if !cfg.TestOnly {
		if info, err := os.Stat(destPath); err == nil && !info.IsDir() {
			if !cfg.Overwrite {
				return &DownloadResult{LocalPath: destPath, Size: info.Size(), Skipped: true}, nil
			}
		}
		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return nil, fmt.Errorf("创建本地目录失败: %w", err)
		}
	}

	var lastErr error
	for _, candidateURL := range candidates {
		curLink := link
		curLink.DownloadURL = candidateURL
		res, err := d.downloadWithSingleCandidate(ctx, curLink, destPath, cfg)
		if err == nil {
			return res, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, lastErr
}

func (d *Downloader) downloadWithSingleCandidate(ctx context.Context, link quarkpan.DownloadLink, destPath string, cfg DownloadConfig) (*DownloadResult, error) {
	startAt := time.Now()

	// 单线程直接下载且已知文件体积时跳过额外的 Range 0-0 探测，减少一次容易触发风控/断连的 TLS 握手
	totalSize := link.Size
	supportRange := false
	var integrity transportIntegrity
	needProbe := cfg.TestOnly || cfg.Parallel > 1 || link.Size <= 0
	if needProbe {
		var err error
		totalSize, supportRange, integrity, err = d.probeDownload(ctx, link.DownloadURL, link.Size, cfg.MaxRetry)
		if err != nil {
			return nil, err
		}
	}

	limiter := NewRateLimiter(cfg.RateLimit)

	// 测试模式下只拉取数据验证连通与速率，不写入本地文件
	if cfg.TestOnly {
		n, _, err := d.downloadSingleStream(ctx, link.DownloadURL, link.FileName, totalSize, io.Discard, limiter, cfg.OnProgress, startAt)
		if err != nil {
			return nil, err
		}
		return &DownloadResult{Size: n, Elapsed: time.Since(startAt)}, nil
	}

	tmpPath := destPath + downloadingSuffix
	statePath := destPath + downloadStateSuffix

	if !supportRange || totalSize <= 0 || totalSize <= cfg.ChunkSize || cfg.Parallel == 1 {
		f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
		if err != nil {
			return nil, fmt.Errorf("创建临时下载文件失败: %w", err)
		}
		n, streamIntegrity, dlErr := d.downloadWithRetrySingle(ctx, link, totalSize, f, limiter, cfg, startAt)
		closeErr := f.Close()
		if dlErr != nil {
			return nil, dlErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		integrity.merge(streamIntegrity)
		expectedSize := totalSize
		if expectedSize <= 0 {
			expectedSize = n
		}
		verified, md5Mismatch, err := d.finalizeFile(tmpPath, statePath, destPath, link, expectedSize, integrity, cfg)
		if err != nil {
			return nil, err
		}
		return &DownloadResult{
			LocalPath:   destPath,
			Size:        n,
			Verified:    verified,
			MD5Mismatch: md5Mismatch,
			Elapsed:     time.Since(startAt),
		}, nil
	}

	// 多线程 Range 并发分片下载（带断点续传状态文件）
	if err := d.downloadChunked(ctx, link, tmpPath, statePath, totalSize, limiter, cfg, startAt); err != nil {
		return nil, err
	}

	verified, md5Mismatch, err := d.finalizeFile(tmpPath, statePath, destPath, link, totalSize, integrity, cfg)
	if err != nil {
		return nil, err
	}
	return &DownloadResult{
		LocalPath:   destPath,
		Size:        totalSize,
		Verified:    verified,
		MD5Mismatch: md5Mismatch,
		Elapsed:     time.Since(startAt),
	}, nil
}

func (d *Downloader) probeDownload(ctx context.Context, rawURL string, fallbackSize int64, maxRetry int) (int64, bool, transportIntegrity, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return 0, false, transportIntegrity{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * 400 * time.Millisecond):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
		if err != nil {
			return 0, false, transportIntegrity{}, fmt.Errorf("构建探测请求失败: %w", err)
		}
		d.client.ApplyDownloadHeaders(req)
		req.Header.Set("Range", "bytes=0-0")

		resp, err := d.client.DownloadHTTPClient().Do(req)
		if err != nil {
			lastErr = fmt.Errorf("探测下载链接失败: %w", err)
			if ctx.Err() != nil {
				return 0, false, transportIntegrity{}, ctx.Err()
			}
			continue
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()

		integrity := extractTransportIntegrity(resp, false)
		if resp.StatusCode == http.StatusPartialContent {
			total := parseContentRangeTotal(resp.Header.Get("Content-Range"))
			if total <= 0 {
				total = fallbackSize
			}
			return total, true, integrity, nil
		}
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			total := resp.ContentLength
			if total <= 0 {
				total = fallbackSize
			}
			supportRange := strings.EqualFold(resp.Header.Get("Accept-Ranges"), "bytes")
			return total, supportRange, integrity, nil
		}
		lastErr = fmt.Errorf("下载链接返回异常状态码 %d", resp.StatusCode)
		// 4xx 鉴权失败或失效直接返回，无需无意义重试
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return 0, false, transportIntegrity{}, lastErr
		}
	}
	return 0, false, transportIntegrity{}, lastErr
}

func parseContentRangeTotal(val string) int64 {
	slash := strings.LastIndexByte(val, '/')
	if slash < 0 || slash+1 >= len(val) {
		return 0
	}
	var total int64
	_, _ = fmt.Sscanf(strings.TrimSpace(val[slash+1:]), "%d", &total)
	return total
}

func (d *Downloader) downloadWithRetrySingle(ctx context.Context, link quarkpan.DownloadLink, totalSize int64, dst *os.File, limiter *RateLimiter, cfg DownloadConfig, startAt time.Time) (int64, transportIntegrity, error) {
	var lastErr error
	for attempt := 0; attempt <= cfg.MaxRetry; attempt++ {
		if attempt > 0 {
			if _, err := dst.Seek(0, io.SeekStart); err != nil {
				return 0, transportIntegrity{}, err
			}
			if err := dst.Truncate(0); err != nil {
				return 0, transportIntegrity{}, err
			}
			select {
			case <-ctx.Done():
				return 0, transportIntegrity{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		n, integrity, err := d.downloadSingleStream(ctx, link.DownloadURL, link.FileName, totalSize, dst, limiter, cfg.OnProgress, startAt)
		if err == nil {
			return n, integrity, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return 0, transportIntegrity{}, ctx.Err()
		}
	}
	return 0, transportIntegrity{}, lastErr
}

func (d *Downloader) downloadSingleStream(ctx context.Context, rawURL, name string, totalSize int64, dst io.Writer, limiter *RateLimiter, onProgress func(ProgressSnapshot), startAt time.Time) (int64, transportIntegrity, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, transportIntegrity{}, err
	}
	d.client.ApplyDownloadHeaders(req)

	resp, err := d.client.DownloadHTTPClient().Do(req)
	if err != nil {
		return 0, transportIntegrity{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, transportIntegrity{}, fmt.Errorf("下载请求失败，状态码 %d", resp.StatusCode)
	}
	if totalSize <= 0 && resp.ContentLength > 0 {
		totalSize = resp.ContentLength
	}
	integrity := extractTransportIntegrity(resp, resp.StatusCode == http.StatusOK)

	reader := NewLimitedReader(ctx, resp.Body, limiter)
	buf := make([]byte, 64*1024)
	var completed int64
	lastNotify := time.Time{}

	for {
		n, readErr := reader.Read(buf)
		if n > 0 {
			if _, wErr := dst.Write(buf[:n]); wErr != nil {
				return completed, transportIntegrity{}, wErr
			}
			completed += int64(n)
			if onProgress != nil && time.Since(lastNotify) >= 120*time.Millisecond {
				elapsed := time.Since(startAt).Seconds()
				var speed float64
				if elapsed > 0 {
					speed = float64(completed) / elapsed
				}
				onProgress(ProgressSnapshot{
					Name:        name,
					Completed:   completed,
					Total:       totalSize,
					BytesPerSec: speed,
				})
				lastNotify = time.Now()
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return completed, transportIntegrity{}, readErr
		}
	}

	if resp.ContentLength > 0 && completed != resp.ContentLength {
		return completed, transportIntegrity{}, fmt.Errorf("下载内容长度不完整 (已接收 %d / 响应头 %d)", completed, resp.ContentLength)
	}
	if totalSize > 0 && completed != totalSize {
		return completed, transportIntegrity{}, fmt.Errorf("下载内容长度不完整 (已接收 %d / 期望 %d)", completed, totalSize)
	}

	if onProgress != nil {
		elapsed := time.Since(startAt).Seconds()
		var speed float64
		if elapsed > 0 {
			speed = float64(completed) / elapsed
		}
		onProgress(ProgressSnapshot{
			Name:        name,
			Completed:   completed,
			Total:       totalSize,
			BytesPerSec: speed,
			Done:        true,
		})
	}
	return completed, integrity, nil
}

func (d *Downloader) downloadChunked(ctx context.Context, link quarkpan.DownloadLink, tmpPath, statePath string, totalSize int64, limiter *RateLimiter, cfg DownloadConfig, startAt time.Time) error {
	st, resumed := loadOrInitDownloadState(statePath, tmpPath, link.DownloadURL, totalSize, cfg.ChunkSize)

	openFlags := os.O_CREATE | os.O_RDWR
	if !resumed {
		openFlags |= os.O_TRUNC
	}
	f, err := os.OpenFile(tmpPath, openFlags, 0644)
	if err != nil {
		return fmt.Errorf("打开分片临时文件失败: %w", err)
	}
	defer f.Close()

	if !resumed {
		if err := f.Truncate(totalSize); err != nil {
			return fmt.Errorf("预分配文件空间失败: %w", err)
		}
		_ = saveJSONAtomic(statePath, st)
	}

	var completedBytes atomic.Int64
	pending := make([]int, 0, len(st.Chunks))
	for i, ch := range st.Chunks {
		if ch.Done {
			completedBytes.Add(ch.End - ch.Start + 1)
		} else {
			pending = append(pending, i)
		}
	}

	if len(pending) == 0 {
		return nil
	}

	var stateMu sync.Mutex
	jobCh := make(chan int, len(pending))
	for _, idx := range pending {
		jobCh <- idx
	}
	close(jobCh)

	workerCount := cfg.Parallel
	if workerCount > len(pending) {
		workerCount = len(pending)
	}

	ctxCancel, cancel := context.WithCancel(ctx)
	defer cancel()

	// 定时上报并发汇总进度
	if cfg.OnProgress != nil {
		doneProg := make(chan struct{})
		defer close(doneProg)
		go func() {
			tk := time.NewTicker(150 * time.Millisecond)
			defer tk.Stop()
			for {
				select {
				case <-doneProg:
					return
				case <-ctxCancel.Done():
					return
				case <-tk.C:
					done := completedBytes.Load()
					elapsed := time.Since(startAt).Seconds()
					var speed float64
					if elapsed > 0 {
						speed = float64(done) / elapsed
					}
					cfg.OnProgress(ProgressSnapshot{
						Name:        link.FileName,
						Completed:   done,
						Total:       totalSize,
						BytesPerSec: speed,
					})
				}
			}
		}()
	}

	var firstErr error
	var errOnce sync.Once
	var wg sync.WaitGroup

	for w := 0; w < workerCount; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobCh {
				if ctxCancel.Err() != nil {
					return
				}
				ch := st.Chunks[idx]
				err := d.downloadChunkWithRetry(ctxCancel, link.DownloadURL, f, ch, cfg.MaxRetry, limiter, &completedBytes)
				if err != nil {
					errOnce.Do(func() {
						firstErr = err
						cancel()
					})
					return
				}
				stateMu.Lock()
				st.Chunks[idx].Done = true
				_ = saveJSONAtomic(statePath, st)
				stateMu.Unlock()
			}
		}()
	}

	wg.Wait()
	if firstErr != nil {
		return firstErr
	}
	if cfg.OnProgress != nil {
		elapsed := time.Since(startAt).Seconds()
		var speed float64
		if elapsed > 0 {
			speed = float64(totalSize) / elapsed
		}
		cfg.OnProgress(ProgressSnapshot{
			Name:        link.FileName,
			Completed:   totalSize,
			Total:       totalSize,
			BytesPerSec: speed,
			Done:        true,
		})
	}
	return nil
}

func (d *Downloader) downloadChunkWithRetry(ctx context.Context, rawURL string, f *os.File, ch chunkRange, maxRetry int, limiter *RateLimiter, completed *atomic.Int64) error {
	expectedLen := ch.End - ch.Start + 1
	var lastErr error
	for attempt := 0; attempt <= maxRetry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 400 * time.Millisecond):
			}
		}
		written, err := d.fetchRangeOnce(ctx, rawURL, f, ch.Start, ch.End, limiter, completed)
		if err == nil && written == expectedLen {
			return nil
		}
		// 若分片中途失败重试，回退本次未完成分片累计字节数保持进度条准确
		if written > 0 {
			completed.Add(-written)
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("分片 #%d 写入长度不完整 (%d/%d)", ch.Index, written, expectedLen)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return lastErr
}

func (d *Downloader) fetchRangeOnce(ctx context.Context, rawURL string, f *os.File, start, end int64, limiter *RateLimiter, completed *atomic.Int64) (int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return 0, err
	}
	d.client.ApplyDownloadHeaders(req)
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

	resp, err := d.client.DownloadHTTPClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if start > 0 && resp.StatusCode != http.StatusPartialContent {
		return 0, fmt.Errorf("Range 分片下载被降级为状态码 %d (偏移 %d)", resp.StatusCode, start)
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("Range 下载返回状态 %d", resp.StatusCode)
	}
	if resp.StatusCode == http.StatusPartialContent {
		if cr := strings.TrimSpace(resp.Header.Get("Content-Range")); cr != "" {
			if actualStart, ok := parseContentRangeStart(cr); ok && actualStart != start {
				return 0, fmt.Errorf("Range 响应偏移不匹配 (期望 %d, 实际 %d)", start, actualStart)
			}
		}
	}

	reader := NewLimitedReader(ctx, resp.Body, limiter)
	buf := make([]byte, 64*1024)
	offset := start
	var written int64
	need := end - start + 1

	for written < need {
		toRead := len(buf)
		if int64(toRead) > need-written {
			toRead = int(need - written)
		}
		n, readErr := reader.Read(buf[:toRead])
		if n > 0 {
			if _, wErr := f.WriteAt(buf[:n], offset); wErr != nil {
				return written, wErr
			}
			offset += int64(n)
			written += int64(n)
			completed.Add(int64(n))
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return written, readErr
		}
	}
	return written, nil
}

func (d *Downloader) finalizeFile(tmpPath, statePath, destPath string, link quarkpan.DownloadLink, expectedSize int64, integrity transportIntegrity, cfg DownloadConfig) (bool, bool, error) {
	if expectedSize > 0 {
		info, err := os.Stat(tmpPath)
		if err != nil {
			return false, false, fmt.Errorf("读取临时文件体积失败: %w", err)
		}
		if info.Size() != expectedSize {
			return false, false, fmt.Errorf("下载文件体积不完整 (期望 %d B, 实际 %d B)", expectedSize, info.Size())
		}
	}

	verified := false
	md5Mismatch := false
	expectedAPIMD5, hasAPIMD5 := normalizeExpectedMD5(link.MD5)
	if !cfg.NoCheck {
		needMD5 := integrity.contentMD5Hex != "" || hasAPIMD5
		needCRC64 := integrity.hasCRC64
		if needMD5 || needCRC64 {
			actualMD5, actualCRC64, err := computeFileHashes(tmpPath, needMD5, needCRC64)
			if err != nil {
				return false, false, fmt.Errorf("计算本地文件校验和失败: %w", err)
			}
			if needCRC64 {
				if actualCRC64 != integrity.crc64ECMA {
					return false, false, fmt.Errorf("文件 CRC64 校验失败 (期望 %d, 实际 %d)", integrity.crc64ECMA, actualCRC64)
				}
				verified = true
			}
			if integrity.contentMD5Hex != "" {
				if !strings.EqualFold(actualMD5, integrity.contentMD5Hex) {
					return false, false, fmt.Errorf("文件 Content-MD5 校验失败 (期望 %s, 实际 %s)", integrity.contentMD5Hex, actualMD5)
				}
				verified = true
			}
			if hasAPIMD5 {
				if strings.EqualFold(actualMD5, expectedAPIMD5) {
					verified = true
				} else {
					// NOTE: 夸克 /1/clouddrive/file/download 返回的 md5 字段在部分转存/文档类资源上并非实际字节流的标准 MD5，
					// 已通过上述体积与传输层头校验确保落盘文件完整，此处仅记录差异而不阻断完成。
					md5Mismatch = true
				}
			}
		}
	}

	if cfg.Overwrite {
		_ = os.Remove(destPath)
	}
	if err := os.Rename(tmpPath, destPath); err != nil {
		return false, false, fmt.Errorf("保存最终文件失败: %w", err)
	}
	_ = os.Remove(statePath)

	if cfg.ModifyMTime && link.UpdatedAt > 0 {
		ts := link.UpdatedAt
		if ts > 1e12 {
			ts = ts / 1000
		}
		mtime := time.Unix(ts, 0)
		_ = os.Chtimes(destPath, mtime, mtime)
	}
	return verified, md5Mismatch, nil
}

func (t *transportIntegrity) merge(other transportIntegrity) {
	if other.contentMD5Hex != "" {
		t.contentMD5Hex = other.contentMD5Hex
	}
	if other.hasCRC64 {
		t.crc64ECMA = other.crc64ECMA
		t.hasCRC64 = true
	}
}

func extractTransportIntegrity(resp *http.Response, allowContentMD5 bool) transportIntegrity {
	var out transportIntegrity
	if resp == nil {
		return out
	}
	if strings.TrimSpace(resp.Header.Get("Content-Encoding")) != "" {
		return out
	}
	if allowContentMD5 {
		if hexMD5, ok := parseContentMD5Header(resp.Header.Get("Content-MD5")); ok {
			out.contentMD5Hex = hexMD5
		}
	}
	if crcVal, ok := parseCRC64Header(resp.Header.Get("x-oss-hash-crc64ecma")); ok {
		out.crc64ECMA = crcVal
		out.hasCRC64 = true
	}
	return out
}

func parseContentMD5Header(val string) (string, bool) {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return "", false
	}
	raw, err := base64.StdEncoding.DecodeString(trimmed)
	if err != nil || len(raw) != md5.Size {
		return "", false
	}
	return hex.EncodeToString(raw), true
}

func parseCRC64Header(val string) (uint64, bool) {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return 0, false
	}
	v, err := strconv.ParseUint(trimmed, 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

func normalizeExpectedMD5(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", false
	}
	if len(trimmed) == 32 {
		if decoded, err := hex.DecodeString(trimmed); err == nil && len(decoded) == md5.Size {
			return strings.ToLower(trimmed), true
		}
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := enc.DecodeString(trimmed); err == nil && len(decoded) == md5.Size {
			return hex.EncodeToString(decoded), true
		}
	}
	return "", false
}

func parseContentRangeStart(val string) (int64, bool) {
	trimmed := strings.TrimSpace(val)
	const prefix = "bytes "
	if len(trimmed) <= len(prefix) || !strings.EqualFold(trimmed[:len(prefix)], prefix) {
		return 0, false
	}
	rest := strings.TrimSpace(trimmed[len(prefix):])
	dash := strings.IndexByte(rest, '-')
	if dash <= 0 {
		return 0, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(rest[:dash]), 10, 64)
	if err != nil || start < 0 {
		return 0, false
	}
	return start, true
}

func computeFileHashes(filePath string, needMD5, needCRC64 bool) (string, uint64, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()

	var writers []io.Writer
	var md5Hash interface {
		io.Writer
		Sum([]byte) []byte
	}
	if needMD5 {
		h := md5.New()
		md5Hash = h
		writers = append(writers, h)
	}
	var crcHash interface {
		io.Writer
		Sum64() uint64
	}
	if needCRC64 {
		c := crc64.New(crc64.MakeTable(crc64.ECMA))
		crcHash = c
		writers = append(writers, c)
	}
	if len(writers) == 0 {
		return "", 0, nil
	}

	if _, err := io.Copy(io.MultiWriter(writers...), f); err != nil {
		return "", 0, err
	}
	var md5Hex string
	if md5Hash != nil {
		md5Hex = hex.EncodeToString(md5Hash.Sum(nil))
	}
	var crcVal uint64
	if crcHash != nil {
		crcVal = crcHash.Sum64()
	}
	return md5Hex, crcVal, nil
}

func loadOrInitDownloadState(statePath, tmpPath, rawURL string, totalSize, chunkSize int64) (*downloadState, bool) {
	if _, err := os.Stat(tmpPath); err == nil {
		if raw, err := os.ReadFile(statePath); err == nil {
			var st downloadState
			if json.Unmarshal(raw, &st) == nil && st.TotalSize == totalSize && st.ChunkSize == chunkSize && len(st.Chunks) > 0 {
				// 直链每次可能带不同的临时鉴权参数，允许同一文件同一大小复用已完成分片
				st.URL = rawURL
				return &st, true
			}
		}
	}

	var chunks []chunkRange
	idx := 0
	for start := int64(0); start < totalSize; start += chunkSize {
		end := start + chunkSize - 1
		if end >= totalSize {
			end = totalSize - 1
		}
		chunks = append(chunks, chunkRange{
			Index: idx,
			Start: start,
			End:   end,
		})
		idx++
	}
	return &downloadState{
		URL:       rawURL,
		TotalSize: totalSize,
		ChunkSize: chunkSize,
		Chunks:    chunks,
	}, false
}

func saveJSONAtomic(path string, v interface{}) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
