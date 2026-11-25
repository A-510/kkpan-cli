package transfer

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kkpan-cli/kkpan/quarkpan"
)

const uploadStateSuffix = ".kkpan.upload.json"

// UploadConfig 控制本地文件上传行为（秒传、并发、重名策略等）
type UploadConfig struct {
	Parallel      int
	MaxRetry      int
	RateLimit     int64
	NoRapidUpload bool
	Policy        string // skip | overwrite | rename
	OnProgress    func(ProgressSnapshot)
}

// UploadResult 返回单个文件上传完成后的状态与云端 FID
type UploadResult struct {
	RemoteName string
	FID        string
	Size       int64
	Rapid      bool
	Skipped    bool
	Elapsed    time.Duration
}

type uploadResumeState struct {
	LocalSize  int64                 `json:"local_size"`
	MD5        string                `json:"md5"`
	SHA1       string                `json:"sha1"`
	TargetDir  string                `json:"target_dir"`
	RemoteName string                `json:"remote_name"`
	Pre        quarkpan.UploadPreResponse `json:"pre"`
	Parts      map[int]string        `json:"parts"`
}

// Uploader 实现流式哈希校验、秒传与阿里云 OSS 多线程分片断点续传
type Uploader struct {
	client *quarkpan.Client
}

// NewUploader 创建上传引擎实例
func NewUploader(client *quarkpan.Client) *Uploader {
	return &Uploader{client: client}
}

// HashFileSinglePass 单遍扫描本地文件同时计算 MD5 与 SHA1 十六进制摘要
func HashFileSinglePass(filePath string) (md5Hex string, sha1Hex string, size int64, err error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", "", 0, err
	}
	defer f.Close()

	hMD5 := md5.New()
	hSHA1 := sha1.New()
	mw := io.MultiWriter(hMD5, hSHA1)

	n, err := io.Copy(mw, f)
	if err != nil {
		return "", "", 0, err
	}
	return hex.EncodeToString(hMD5.Sum(nil)), hex.EncodeToString(hSHA1.Sum(nil)), n, nil
}

// UploadFile 上传单个本地文件至目标网盘目录 FID
func (u *Uploader) UploadFile(ctx context.Context, localPath, pdirFID string, cfg UploadConfig) (*UploadResult, error) {
	stat, err := os.Stat(localPath)
	if err != nil {
		return nil, fmt.Errorf("读取本地文件失败: %w", err)
	}
	if stat.IsDir() {
		return nil, fmt.Errorf("UploadFile 不直接处理目录: %s", localPath)
	}
	if cfg.Parallel <= 0 {
		cfg.Parallel = 2
	}
	if cfg.MaxRetry < 0 {
		cfg.MaxRetry = 0
	}
	policy := strings.ToLower(strings.TrimSpace(cfg.Policy))
	if policy == "" {
		policy = "skip"
	}

	startAt := time.Now()
	baseName := filepath.Base(localPath)

	remoteName, skip, err := u.resolveConflict(ctx, pdirFID, baseName, policy)
	if err != nil {
		return nil, err
	}
	if skip {
		return &UploadResult{RemoteName: baseName, Size: stat.Size(), Skipped: true}, nil
	}

	md5Hex, sha1Hex, fileSize, err := HashFileSinglePass(localPath)
	if err != nil {
		return nil, fmt.Errorf("计算本地文件哈希失败: %w", err)
	}

	mimeType := "application/octet-stream"
	if rawMIME := mime.TypeByExtension(filepath.Ext(localPath)); rawMIME != "" {
		if mediaType, _, parseErr := mime.ParseMediaType(rawMIME); parseErr == nil && strings.TrimSpace(mediaType) != "" {
			mimeType = mediaType
		}
	}

	statePath := localPath + uploadStateSuffix
	state := u.loadResumeState(statePath, fileSize, md5Hex, sha1Hex, pdirFID, remoteName)

	var pre *quarkpan.UploadPreResponse
	if state != nil {
		preCopy := state.Pre
		pre = &preCopy
	} else {
		pre, err = u.client.UploadPre(ctx, pdirFID, remoteName, mimeType, fileSize, stat.ModTime().UnixMilli())
		if err != nil {
			return nil, err
		}
		if pre.Finish {
			if cfg.OnProgress != nil {
				cfg.OnProgress(ProgressSnapshot{Name: remoteName, Completed: fileSize, Total: fileSize, Done: true})
			}
			return &UploadResult{
				RemoteName: remoteName,
				FID:        pre.FID,
				Size:       fileSize,
				Rapid:      true,
				Elapsed:    time.Since(startAt),
			}, nil
		}

		if !cfg.NoRapidUpload {
			rapidOK, rapidFID, err := u.client.UploadUpdateHash(ctx, pre.TaskID, md5Hex, sha1Hex)
			if err != nil {
				return nil, err
			}
			if rapidOK {
				if cfg.OnProgress != nil {
					cfg.OnProgress(ProgressSnapshot{Name: remoteName, Completed: fileSize, Total: fileSize, Done: true})
				}
				return &UploadResult{
					RemoteName: remoteName,
					FID:        rapidFID,
					Size:       fileSize,
					Rapid:      true,
					Elapsed:    time.Since(startAt),
				}, nil
			}
		}

		state = &uploadResumeState{
			LocalSize:  fileSize,
			MD5:        md5Hex,
			SHA1:       sha1Hex,
			TargetDir:  pdirFID,
			RemoteName: remoteName,
			Pre:        *pre,
			Parts:      make(map[int]string),
		}
		_ = saveJSONAtomic(statePath, state)
	}

	partSize := pre.PartSize
	if partSize <= 0 {
		partSize = quarkpan.DefaultUploadPartSize
	}

	// 空文件或单分片边界至少分配 1 个分片序号
	totalParts := 1
	if fileSize > 0 {
		totalParts = int((fileSize + partSize - 1) / partSize)
	}

	if err := u.uploadPartsConcurrent(ctx, localPath, remoteName, fileSize, partSize, totalParts, mimeType, pre, state, statePath, cfg, startAt); err != nil {
		return nil, err
	}

	parts := make([]quarkpan.UploadPart, 0, totalParts)
	for p := 1; p <= totalParts; p++ {
		etag := state.Parts[p]
		parts = append(parts, quarkpan.UploadPart{PartNumber: p, ETag: etag})
	}

	if err := u.client.CompleteOSSMultipart(ctx, pre, parts); err != nil {
		return nil, err
	}
	fid, err := u.client.UploadFinish(ctx, pre.ObjKey, pre.TaskID, pdirFID)
	if err != nil {
		return nil, err
	}
	_ = os.Remove(statePath)

	if cfg.OnProgress != nil {
		elapsed := time.Since(startAt).Seconds()
		var speed float64
		if elapsed > 0 {
			speed = float64(fileSize) / elapsed
		}
		cfg.OnProgress(ProgressSnapshot{
			Name:        remoteName,
			Completed:   fileSize,
			Total:       fileSize,
			BytesPerSec: speed,
			Done:        true,
		})
	}

	return &UploadResult{
		RemoteName: remoteName,
		FID:        fid,
		Size:       fileSize,
		Elapsed:    time.Since(startAt),
	}, nil
}

func (u *Uploader) resolveConflict(ctx context.Context, pdirFID, baseName, policy string) (string, bool, error) {
	items, err := u.client.ListDirByFID(ctx, pdirFID, &quarkpan.ListOptions{ForceRefresh: true})
	if err != nil {
		return "", false, err
	}
	existing := make(map[string]quarkpan.FileItem, len(items))
	for _, it := range items {
		existing[it.FileName] = it
	}
	conflictItem, exists := existing[baseName]
	if !exists {
		return baseName, false, nil
	}

	switch policy {
	case "skip":
		return baseName, true, nil
	case "overwrite":
		if conflictItem.IsDir {
			return "", false, fmt.Errorf("云端已存在同名目录，拒绝覆盖: %s", baseName)
		}
		if err := u.client.Delete(ctx, []string{conflictItem.FID}); err != nil {
			return "", false, fmt.Errorf("覆盖前删除旧文件失败: %w", err)
		}
		return baseName, false, nil
	case "rename":
		ext := filepath.Ext(baseName)
		stem := strings.TrimSuffix(baseName, ext)
		for i := 1; i < 1000; i++ {
			candidate := fmt.Sprintf("%s (%d)%s", stem, i, ext)
			if _, taken := existing[candidate]; !taken {
				return candidate, false, nil
			}
		}
		return "", false, fmt.Errorf("无法为 %s 生成不冲突的新文件名", baseName)
	default:
		return "", false, fmt.Errorf("未知的重名处理策略 %q (可选: skip, overwrite, rename)", policy)
	}
}

func (u *Uploader) loadResumeState(statePath string, size int64, md5Hex, sha1Hex, targetDir, remoteName string) *uploadResumeState {
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return nil
	}
	var st uploadResumeState
	if json.Unmarshal(raw, &st) != nil {
		return nil
	}
	if st.LocalSize != size || st.MD5 != md5Hex || st.SHA1 != sha1Hex || st.TargetDir != targetDir || st.RemoteName != remoteName || st.Pre.UploadID == "" {
		return nil
	}
	if st.Parts == nil {
		st.Parts = make(map[int]string)
	}
	return &st
}

func (u *Uploader) uploadPartsConcurrent(
	ctx context.Context,
	localPath, remoteName string,
	fileSize, partSize int64,
	totalParts int,
	mimeType string,
	pre *quarkpan.UploadPreResponse,
	state *uploadResumeState,
	statePath string,
	cfg UploadConfig,
	startAt time.Time,
) error {
	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	var uploadedBytes atomic.Int64
	pending := make([]int, 0, totalParts)
	for p := 1; p <= totalParts; p++ {
		partLen := calcPartLength(fileSize, partSize, p)
		if _, ok := state.Parts[p]; ok {
			uploadedBytes.Add(partLen)
		} else {
			pending = append(pending, p)
		}
	}
	if len(pending) == 0 {
		return nil
	}

	limiter := NewRateLimiter(cfg.RateLimit)
	jobCh := make(chan int, len(pending))
	for _, p := range pending {
		jobCh <- p
	}
	close(jobCh)

	workers := cfg.Parallel
	if workers > len(pending) {
		workers = len(pending)
	}

	ctxCancel, cancel := context.WithCancel(ctx)
	defer cancel()

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
					done := uploadedBytes.Load()
					elapsed := time.Since(startAt).Seconds()
					var speed float64
					if elapsed > 0 {
						speed = float64(done) / elapsed
					}
					cfg.OnProgress(ProgressSnapshot{
						Name:        remoteName,
						Completed:   done,
						Total:       fileSize,
						BytesPerSec: speed,
					})
				}
			}
		}()
	}

	var stateMu sync.Mutex
	var firstErr error
	var errOnce sync.Once
	var wg sync.WaitGroup

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for partNum := range jobCh {
				if ctxCancel.Err() != nil {
					return
				}
				offset := int64(partNum-1) * partSize
				length := calcPartLength(fileSize, partSize, partNum)
				etag, err := u.uploadSinglePartRetry(ctxCancel, f, offset, length, partNum, mimeType, pre, limiter, cfg.MaxRetry)
				if err != nil {
					errOnce.Do(func() {
						firstErr = err
						cancel()
					})
					return
				}
				uploadedBytes.Add(length)
				stateMu.Lock()
				state.Parts[partNum] = etag
				_ = saveJSONAtomic(statePath, state)
				stateMu.Unlock()
			}
		}()
	}
	wg.Wait()
	return firstErr
}

func calcPartLength(fileSize, partSize int64, partNum int) int64 {
	if fileSize == 0 {
		return 0
	}
	offset := int64(partNum-1) * partSize
	rem := fileSize - offset
	if rem > partSize {
		return partSize
	}
	return rem
}

func (u *Uploader) uploadSinglePartRetry(
	ctx context.Context,
	f *os.File,
	offset, length int64,
	partNum int,
	mimeType string,
	pre *quarkpan.UploadPreResponse,
	limiter *RateLimiter,
	maxRetry int,
) (string, error) {
	var lastErr error
	for attempt := 0; attempt <= maxRetry; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 400 * time.Millisecond):
			}
		}
		sr := io.NewSectionReader(f, offset, length)
		lr := NewLimitedReader(ctx, sr, limiter)
		etag, err := u.client.UploadOSSPart(ctx, pre, partNum, lr, length, mimeType)
		if err == nil {
			return etag, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
	}
	return "", lastErr
}
