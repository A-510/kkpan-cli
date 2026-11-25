package kkcommand

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kkpan-cli/kkpan/internal/kkconfig"
	"github.com/kkpan-cli/kkpan/internal/transfer"
	"github.com/kkpan-cli/kkpan/internal/ui"
)

// UploadOptions 控制上传命令参数
type UploadOptions struct {
	Parallel      int
	Load          int
	MaxRetry      int
	NoRapidUpload bool
	Policy        string // skip | overwrite | rename
}

type uploadTask struct {
	localPath string
	targetDir string
}

// RunUpload 将本地一个或多个文件/目录上传至目标网盘目录
func RunUpload(localPaths []string, remoteDir string, opt *UploadOptions) error {
	if len(localPaths) == 0 {
		return fmt.Errorf("请指定至少一个本地文件或目录")
	}
	if opt == nil {
		opt = &UploadOptions{}
	}
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}

	parallel := opt.Parallel
	if parallel <= 0 {
		parallel = kkconfig.Manager.MaxUploadParallel
	}
	load := opt.Load
	if load <= 0 {
		load = kkconfig.Manager.MaxUploadLoad
	}
	maxRetry := opt.MaxRetry
	if maxRetry <= 0 {
		maxRetry = 3
	}
	policy := strings.TrimSpace(opt.Policy)
	if policy == "" {
		policy = kkconfig.Manager.UPolicy
	}

	normRemote := resolveInputPath(remoteDir)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var tasks []uploadTask
	for _, lp := range localPaths {
		absLocal, err := filepath.Abs(lp)
		if err != nil {
			return err
		}
		stat, err := os.Stat(absLocal)
		if err != nil {
			return fmt.Errorf("读取本地路径失败: %w", err)
		}
		if !stat.IsDir() {
			tasks = append(tasks, uploadTask{localPath: absLocal, targetDir: normRemote})
			continue
		}
		baseRoot := filepath.Dir(absLocal)
		if err := filepath.WalkDir(absLocal, func(p string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(baseRoot, filepath.Dir(p))
			if err != nil {
				return err
			}
			dstDir := normRemote
			if rel != "." {
				dstDir = path.Join(normRemote, filepath.ToSlash(rel))
			}
			tasks = append(tasks, uploadTask{localPath: p, targetDir: dstDir})
			return nil
		}); err != nil {
			return err
		}
	}

	if len(tasks) == 0 {
		ui.PrintInfo(os.Stdout, "本地目录内没有可上传的文件")
		return nil
	}

	// 预先创建所需目标目录并缓存目录 FID，避免并发重复建目录
	dirFIDCache := make(map[string]string)
	var dirMu sync.Mutex
	ensureDirFID := func(dirPath string) (string, error) {
		dirMu.Lock()
		defer dirMu.Unlock()
		if fid, ok := dirFIDCache[dirPath]; ok {
			return fid, nil
		}
		fid, err := cli.MkdirAll(ctx, dirPath)
		if err != nil {
			return "", err
		}
		dirFIDCache[dirPath] = fid
		return fid, nil
	}

	up := transfer.NewUploader(cli)
	pp := ui.NewProgressPrinter(os.Stderr)
	sem := make(chan struct{}, load)
	var wg sync.WaitGroup
	var errMu sync.Mutex
	var firstErr error

	for _, task := range tasks {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(tk uploadTask) {
			defer wg.Done()
			defer func() { <-sem }()

			pdirFID, err := ensureDirFID(tk.targetDir)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}

			res, err := up.UploadFile(ctx, tk.localPath, pdirFID, transfer.UploadConfig{
				Parallel:      parallel,
				MaxRetry:      maxRetry,
				RateLimit:     kkconfig.Manager.MaxUploadRate,
				NoRapidUpload: opt.NoRapidUpload,
				Policy:        policy,
				OnProgress: func(snap transfer.ProgressSnapshot) {
					pp.Update(snap.Name, snap.Completed, snap.Total, snap.BytesPerSec, snap.Done)
				},
			})
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			switch {
			case res.Skipped:
				ui.PrintWarn(os.Stderr, "云端已存在同名文件，已跳过: %s/%s", tk.targetDir, res.RemoteName)
			case res.Rapid:
				ui.PrintSuccess(os.Stdout, "秒传成功: %s -> %s/%s (%s)", tk.localPath, tk.targetDir, res.RemoteName, ui.FormatBytes(res.Size))
			default:
				ui.PrintSuccess(os.Stdout, "上传完成: %s -> %s/%s (%s, 耗时 %s)", tk.localPath, tk.targetDir, res.RemoteName, ui.FormatBytes(res.Size), res.Elapsed.Round(time.Millisecond))
			}
		}(task)
	}
	wg.Wait()
	return firstErr
}
