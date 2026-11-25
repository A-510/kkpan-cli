package kkcommand

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kkpan-cli/kkpan/internal/kkconfig"
	"github.com/kkpan-cli/kkpan/internal/transfer"
	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
)

// DownloadOptions 控制下载命令参数
type DownloadOptions struct {
	SaveTo      string
	Parallel    int
	Load        int
	MaxRetry    int
	Overwrite   bool
	NoCheck     bool
	ModifyMTime bool
	FullPath    bool
	TestOnly    bool
}

// LocateView 描述直链提取结果（仅提供下载 URL 与合规 Header 提示，不暴露完整 Cookie 明文）
type LocateView struct {
	FID         string `json:"fid"`
	FileName    string `json:"file_name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"download_url"`
	Referer     string `json:"referer"`
	UserAgent   string `json:"user_agent"`
}

// RunLocate 提取指定文件直链
func RunLocate(targets []string, mode ui.OutputMode) error {
	if len(targets) == 0 {
		return fmt.Errorf("请指定待提取直链的文件路径")
	}
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	fids := make([]string, 0, len(targets))
	for _, t := range targets {
		item, err := cli.Resolver().ResolvePath(ctx, resolveInputPath(t))
		if err != nil {
			return err
		}
		if item.IsDir {
			return fmt.Errorf("目录不支持提取直链: %s", item.FullPath)
		}
		fids = append(fids, item.FID)
	}

	links, err := cli.GetDownloadLinks(ctx, fids)
	if err != nil {
		return err
	}

	views := make([]LocateView, 0, len(links))
	for _, lk := range links {
		views = append(views, LocateView{
			FID:         lk.FID,
			FileName:    lk.FileName,
			Size:        lk.Size,
			DownloadURL: lk.DownloadURL,
			Referer:     quarkpan.DefaultReferer,
			UserAgent:   cli.UserAgent(),
		})
	}

	switch mode {
	case ui.OutputJSON:
		return ui.WriteJSON(os.Stdout, views)
	case ui.OutputPlain:
		rows := make([][]string, 0, len(views))
		for _, v := range views {
			rows = append(rows, []string{v.FID, v.FileName, strconv.FormatInt(v.Size, 10), v.DownloadURL})
		}
		return ui.WriteTSV(os.Stdout, rows)
	default:
		rows := make([][]string, 0, len(views))
		for _, v := range views {
			rows = append(rows, []string{v.FileName, ui.FormatBytes(v.Size), v.DownloadURL})
		}
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Headers: []string{"文件名", "大小", "临时直链 (需携带登录 Cookie / Referer)"},
			Rows:    rows,
		}))
		return nil
	}
}

type downloadTask struct {
	item     quarkpan.FileItem
	destPath string
}

// RunDownload 递归展开网盘文件/目录并执行并发分片下载
func RunDownload(targets []string, opt *DownloadOptions) error {
	if len(targets) == 0 {
		return fmt.Errorf("请指定待下载的网盘文件或目录")
	}
	if opt == nil {
		opt = &DownloadOptions{}
	}
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}

	saveDir := strings.TrimSpace(opt.SaveTo)
	if saveDir == "" {
		saveDir = kkconfig.Manager.SaveDir
	}
	parallel := opt.Parallel
	if parallel <= 0 {
		parallel = kkconfig.Manager.MaxParallel
	}
	load := opt.Load
	if load <= 0 {
		load = kkconfig.Manager.MaxDownloadLoad
	}
	maxRetry := opt.MaxRetry
	if maxRetry <= 0 {
		maxRetry = 3
	}
	noCheck := opt.NoCheck || kkconfig.Manager.NoCheck
	modifyMTime := opt.ModifyMTime || kkconfig.Manager.ModifyMTime

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var tasks []downloadTask
	for _, t := range targets {
		norm := resolveInputPath(t)
		it, err := cli.Resolver().ResolvePath(ctx, norm)
		if err != nil {
			return err
		}
		if err := collectDownloadTasks(ctx, cli, *it, saveDir, opt.FullPath, &tasks); err != nil {
			return err
		}
	}
	if len(tasks) == 0 {
		ui.PrintInfo(os.Stdout, "没有需要下载的文件")
		return nil
	}

	dl := transfer.NewDownloader(cli)
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
		go func(tk downloadTask) {
			defer wg.Done()
			defer func() { <-sem }()

			links, err := cli.GetDownloadLinks(ctx, []string{tk.item.FID})
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			link := links[0]
			if link.MD5 == "" {
				link.MD5 = tk.item.MD5
			}
			if link.UpdatedAt == 0 {
				link.UpdatedAt = tk.item.UpdatedAt
			}

			res, err := dl.DownloadFile(ctx, link, tk.destPath, transfer.DownloadConfig{
				Parallel:    parallel,
				MaxRetry:    maxRetry,
				RateLimit:   kkconfig.Manager.MaxDownloadRate,
				Overwrite:   opt.Overwrite,
				NoCheck:     noCheck,
				ModifyMTime: modifyMTime,
				TestOnly:    opt.TestOnly,
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
			if res.Skipped {
				ui.PrintWarn(os.Stderr, "已跳过同名文件: %s", res.LocalPath)
			} else if opt.TestOnly {
				ui.PrintSuccess(os.Stdout, "测速完成: %s (%s, 耗时 %s)", tk.item.FileName, ui.FormatBytes(res.Size), res.Elapsed.Round(time.Millisecond))
			} else {
				ui.PrintSuccess(os.Stdout, "下载完成: %s (%s)", res.LocalPath, ui.FormatBytes(res.Size))
				if res.MD5Mismatch {
					ui.PrintWarn(os.Stderr, "提示: %s 云端元数据 MD5 与本地不一致（夸克部分资源差异），文件体积已校验完整", tk.item.FileName)
				}
			}
		}(task)
	}
	wg.Wait()
	return firstErr
}

func collectDownloadTasks(ctx context.Context, cli *quarkpan.Client, item quarkpan.FileItem, baseDir string, fullPath bool, out *[]downloadTask) error {
	if !item.IsDir {
		dest := filepath.Join(baseDir, item.FileName)
		if fullPath && item.FullPath != "" && !strings.HasPrefix(item.FullPath, quarkpan.FIDPrefix) {
			rel := strings.TrimPrefix(item.FullPath, "/")
			dest = filepath.Join(baseDir, filepath.FromSlash(rel))
		}
		*out = append(*out, downloadTask{item: item, destPath: dest})
		return nil
	}

	nextBase := baseDir
	if !fullPath && item.FileName != "" && item.FileName != "/" {
		nextBase = filepath.Join(baseDir, item.FileName)
	}
	children, err := cli.ListDirByFID(ctx, item.FID, nil)
	if err != nil {
		return err
	}
	for _, ch := range children {
		ch.FullPath = resolveChildPath(item.FullPath, ch.FileName)
		if err := collectDownloadTasks(ctx, cli, ch, nextBase, fullPath, out); err != nil {
			return err
		}
	}
	return nil
}
