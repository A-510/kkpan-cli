package kkcommand

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kkpan-cli/kkpan/internal/kkconfig"
	"github.com/kkpan-cli/kkpan/internal/transfer"
	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
)

const maxCloudTorrentSize = 20 * 1024 * 1024

// BTOptions 控制 bt 离线/种子磁力命令参数
type BTOptions struct {
	SaveTo             string
	ListOnly           bool
	FilesExpr          string
	MatchExpr          string
	All                bool
	UploadTo           string
	DeleteLocal        bool
	RateLimitRaw       string
	MetadataTimeoutSec int
	TrackersRaw        string
	NoDefaultTrackers  bool
	ListenPort         int
}

// BTResultView 描述 bt 下载（及可选网盘上传）的结构化输出
type BTResultView struct {
	InfoHash      string                           `json:"info_hash"`
	Name          string                           `json:"name"`
	SaveDir       string                           `json:"save_dir"`
	TotalSize     int64                            `json:"total_size"`
	Files         []transfer.DownloadedTorrentFile `json:"files"`
	UploadedTo    string                           `json:"uploaded_to,omitempty"`
	UploadedCount int                              `json:"uploaded_count,omitempty"`
	LocalDeleted  bool                             `json:"local_deleted,omitempty"`
}

// RunBT 解析并下载本地/网盘/远程 .torrent 种子或磁力链接，可选自动上传至夸克网盘
func RunBT(args []string, opt *BTOptions, mode ui.OutputMode) error {
	if len(args) == 0 {
		return fmt.Errorf("请提供 .torrent 种子文件路径、磁力链接、InfoHash 或网盘种子路径")
	}
	if opt == nil {
		opt = &BTOptions{}
	}

	rawInput := strings.TrimSpace(strings.Join(args, " "))
	if rawInput == "" {
		return fmt.Errorf("种子或磁力输入不能为空")
	}

	// 前置 fail-fast 校验，避免长时间下载完成后才因参数或登录态报错
	if opt.DeleteLocal && strings.TrimSpace(opt.UploadTo) == "" {
		return fmt.Errorf("--delete-local 必须与 --upload 配合使用")
	}

	var rateLimit int64
	if strings.TrimSpace(opt.RateLimitRaw) != "" {
		parsed, err := kkconfig.ParseByteRate(opt.RateLimitRaw)
		if err != nil {
			return err
		}
		rateLimit = parsed
	} else {
		rateLimit = kkconfig.Manager.MaxDownloadRate
	}

	var uploadClient *quarkpan.Client
	if strings.TrimSpace(opt.UploadTo) != "" {
		cli, err := requireActiveClient()
		if err != nil {
			return err
		}
		uploadClient = cli
	}

	saveDir := strings.TrimSpace(opt.SaveTo)
	if saveDir == "" {
		saveDir = kkconfig.Manager.SaveDir
	}

	metaTimeout := 90 * time.Second
	if opt.MetadataTimeoutSec > 0 {
		metaTimeout = time.Duration(opt.MetadataTimeoutSec) * time.Second
	}

	var extraTrackers []string
	if strings.TrimSpace(opt.TrackersRaw) != "" {
		for _, seg := range strings.Split(opt.TrackersRaw, ",") {
			if tr := strings.TrimSpace(seg); tr != "" {
				extraTrackers = append(extraTrackers, tr)
			}
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// 解析来源：支持本地 .torrent、磁力链接、裸 InfoHash、HTTP URL 及夸克网盘内 .torrent
	rawTorrentBytes, parsedSrc, err := resolveBTSourceInput(ctx, rawInput)
	if err != nil {
		return err
	}

	dlCfg := transfer.TorrentDownloadConfig{
		SaveDir:           saveDir,
		FilesExpr:         opt.FilesExpr,
		MatchExpr:         opt.MatchExpr,
		RateLimit:         rateLimit,
		MetadataTimeout:   metaTimeout,
		ListenPort:        opt.ListenPort,
		ProxyURL:          kkconfig.Manager.Proxy,
		ExtraTrackers:     extraTrackers,
		NoDefaultTrackers: opt.NoDefaultTrackers,
	}

	if opt.ListOnly {
		var info *transfer.TorrentInfo
		if len(rawTorrentBytes) > 0 {
			info, err = transfer.InspectTorrentBytes(rawTorrentBytes)
		} else {
			if parsedSrc != nil && parsedSrc.Kind == transfer.SourceMagnet && !ui.IsQuiet() && mode == ui.OutputPretty {
				ui.PrintInfo(os.Stderr, "正在连接 Peer/DHT 解析磁力链接元数据（最长等待 %s）...", metaTimeout)
			}
			info, err = transfer.InspectTorrent(ctx, rawInput, dlCfg)
		}
		if err != nil {
			return err
		}
		return renderTorrentInfo(info, mode)
	}

	if parsedSrc != nil && parsedSrc.Kind == transfer.SourceMagnet && !ui.IsQuiet() {
		ui.PrintInfo(os.Stderr, "正在解析磁力链接元数据（最长等待 %s）...", metaTimeout)
	}

	dlCfg.OnMetadata = func(info *transfer.TorrentInfo) {
		if !ui.IsQuiet() && mode == ui.OutputPretty {
			ui.PrintInfo(os.Stderr, "已解析种子: %s (Hash: %s, 共 %d 个文件, 总计 %s)",
				info.Name, info.InfoHash, len(info.Files), ui.FormatBytes(info.TotalSize))
		}
	}

	// 仅在交互 TTY、未指定过滤条件且无自动化参数时，对多文件种子弹出一次选择提示
	allowInteractiveSelect := !opt.All &&
		strings.TrimSpace(opt.FilesExpr) == "" &&
		strings.TrimSpace(opt.MatchExpr) == "" &&
		mode == ui.OutputPretty &&
		!ui.IsQuiet() &&
		ui.IsTTY(os.Stdin) &&
		ui.IsTTY(os.Stderr)

	if allowInteractiveSelect {
		dlCfg.OnSelectFiles = promptSelectTorrentFiles
	}

	pp := ui.NewProgressPrinter(os.Stderr)
	dlCfg.OnProgress = func(snap transfer.TorrentProgress) {
		label := snap.Name
		if !snap.Done && snap.TotalPeers > 0 {
			label = fmt.Sprintf("%s [%d/%d]", snap.Name, snap.ActivePeers, snap.TotalPeers)
		}
		pp.Update(label, snap.Completed, snap.Total, snap.BytesPerSec, snap.Done)
	}

	res, err := transfer.DownloadTorrent(ctx, rawInput, rawTorrentBytes, dlCfg)
	if err != nil {
		return err
	}

	view := BTResultView{
		InfoHash:  res.Info.InfoHash,
		Name:      res.Info.Name,
		SaveDir:   res.SaveDir,
		TotalSize: res.TotalSize,
		Files:     res.Files,
	}

	if strings.TrimSpace(opt.UploadTo) != "" {
		normRemote := resolveInputPath(opt.UploadTo)
		uploadedCount, upErr := uploadDownloadedTorrentFiles(ctx, uploadClient, res.Files, normRemote)
		if upErr != nil {
			return upErr
		}
		view.UploadedTo = normRemote
		view.UploadedCount = uploadedCount

		if opt.DeleteLocal {
			cleanupLocalTorrentFiles(res.SaveDir, res.Files)
			view.LocalDeleted = true
		}
	}

	return renderBTDownloadResult(view, res.Elapsed, mode)
}

func resolveBTSourceInput(ctx context.Context, rawInput string) ([]byte, *transfer.TorrentSource, error) {
	src, err := transfer.ParseTorrentSource(rawInput)
	if err == nil {
		return nil, src, nil
	}

	if !shouldTryCloudTorrent(rawInput) {
		return nil, nil, err
	}

	cli, accErr := requireActiveClient()
	if accErr != nil {
		// 若未登录且本地也找不到文件，若明显像本地相对路径则返回原本地错误，否则提示未登录
		if strings.HasPrefix(rawInput, "/") || strings.HasPrefix(strings.ToLower(rawInput), quarkpan.FIDPrefix) {
			return nil, nil, accErr
		}
		return nil, nil, err
	}

	cloudPath := resolveInputPath(rawInput)
	item, resErr := cli.Resolver().ResolvePath(ctx, cloudPath)
	if resErr != nil {
		return nil, nil, fmt.Errorf("未在本地或网盘找到种子文件 %q: %w", rawInput, resErr)
	}
	if item.IsDir {
		return nil, nil, fmt.Errorf("网盘目标是一个目录而非 .torrent 文件: %s", item.FullPath)
	}
	if item.Size > maxCloudTorrentSize {
		return nil, nil, fmt.Errorf("网盘种子文件过大 (%s)，超过 20MB 限制", ui.FormatBytes(item.Size))
	}

	rawBytes, dlErr := fetchCloudTorrentBytes(ctx, cli, item.FID)
	if dlErr != nil {
		return nil, nil, dlErr
	}
	if !ui.IsQuiet() {
		ui.PrintInfo(os.Stderr, "已从夸克网盘读取种子文件: %s (%s)", item.FullPath, ui.FormatBytes(int64(len(rawBytes))))
	}
	return rawBytes, nil, nil
}

func shouldTryCloudTorrent(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, quarkpan.FIDPrefix) {
		return true
	}
	if !strings.HasSuffix(lower, ".torrent") {
		return false
	}
	// 排除 Windows 绝对路径（如 C:\a.torrent）
	if len(trimmed) >= 2 && trimmed[1] == ':' {
		return false
	}
	return true
}

func fetchCloudTorrentBytes(ctx context.Context, cli *quarkpan.Client, fid string) ([]byte, error) {
	links, err := cli.GetDownloadLinks(ctx, []string{fid})
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("未获取到网盘种子直链")
	}

	var lastErr error
	for _, candidate := range links[0].CandidateURLs() {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate, nil)
		if err != nil {
			lastErr = err
			continue
		}
		cli.ApplyDownloadHeaders(req)
		resp, err := cli.DownloadHTTPClient().Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxCloudTorrentSize+1))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			lastErr = fmt.Errorf("拉取网盘种子返回状态码 %d", resp.StatusCode)
			continue
		}
		if readErr != nil {
			lastErr = readErr
			continue
		}
		if len(raw) > maxCloudTorrentSize {
			return nil, fmt.Errorf("网盘种子文件超过 20MB 限制")
		}
		return raw, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("网盘种子下载链接不可用")
	}
	return nil, lastErr
}

func promptSelectTorrentFiles(info *transfer.TorrentInfo) ([]int, error) {
	if info == nil || len(info.Files) <= 1 {
		return nil, nil
	}

	rows := make([][]string, 0, len(info.Files))
	for _, f := range info.Files {
		rows = append(rows, []string{
			fmt.Sprintf("%02d", f.Index),
			f.DisplayPath,
			ui.FormatBytes(f.Size),
		})
	}
	fmt.Fprint(os.Stderr, ui.RenderTable(os.Stderr, ui.TableConfig{
		Headers:    []string{"#", "文件列表", "大小"},
		Rows:       rows,
		Alignments: []ui.TextAlign{ui.AlignRight, ui.AlignLeft, ui.AlignRight},
	}))
	fmt.Fprintf(os.Stderr, "请输入要下载的文件序号 (如 1,3-5，直接回车全部下载，输入 q 取消): ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, fmt.Errorf("读取选择输入失败: %w", err)
	}
	trimmed := strings.TrimSpace(line)
	if trimmed == "" {
		return nil, nil
	}
	if strings.EqualFold(trimmed, "q") || strings.EqualFold(trimmed, "quit") || strings.EqualFold(trimmed, "cancel") {
		return nil, fmt.Errorf("用户取消下载")
	}
	return transfer.ParseFileIndexExpr(trimmed, len(info.Files))
}

func renderTorrentInfo(info *transfer.TorrentInfo, mode ui.OutputMode) error {
	switch mode {
	case ui.OutputJSON:
		return ui.WriteJSON(os.Stdout, info)
	case ui.OutputPlain:
		rows := make([][]string, 0, len(info.Files))
		for _, f := range info.Files {
			rows = append(rows, []string{
				fmt.Sprintf("%d", f.Index),
				fmt.Sprintf("%d", f.Size),
				f.DisplayPath,
			})
		}
		return ui.WriteTSV(os.Stdout, rows)
	default:
		ui.PrintInfo(os.Stdout, "种子名称: %s", info.Name)
		ui.PrintInfo(os.Stdout, "InfoHash: %s | 文件数: %d | 总大小: %s", info.InfoHash, len(info.Files), ui.FormatBytes(info.TotalSize))
		rows := make([][]string, 0, len(info.Files))
		for _, f := range info.Files {
			rows = append(rows, []string{
				fmt.Sprintf("%02d", f.Index),
				f.DisplayPath,
				ui.FormatBytes(f.Size),
			})
		}
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Headers:    []string{"#", "文件名 / 路径", "大小"},
			Rows:       rows,
			Alignments: []ui.TextAlign{ui.AlignRight, ui.AlignLeft, ui.AlignRight},
		}))
		return nil
	}
}

func uploadDownloadedTorrentFiles(
	ctx context.Context,
	cli *quarkpan.Client,
	files []transfer.DownloadedTorrentFile,
	normRemoteRoot string,
) (int, error) {
	if len(files) == 0 {
		return 0, nil
	}

	parallel := kkconfig.Manager.MaxUploadParallel
	if parallel <= 0 {
		parallel = 2
	}
	load := kkconfig.Manager.MaxUploadLoad
	if load <= 0 {
		load = 2
	}
	policy := kkconfig.Manager.UPolicy
	if policy == "" {
		policy = "skip"
	}

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
	var uploadedCount int

	for _, df := range files {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(item transfer.DownloadedTorrentFile) {
			defer wg.Done()
			defer func() { <-sem }()

			relDir := path.Dir(item.RelPath)
			targetDir := normRemoteRoot
			if relDir != "." && relDir != "/" && relDir != "" {
				targetDir = path.Join(normRemoteRoot, relDir)
			}

			pdirFID, err := ensureDirFID(targetDir)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}

			res, err := up.UploadFile(ctx, item.LocalPath, pdirFID, transfer.UploadConfig{
				Parallel:  parallel,
				MaxRetry:  3,
				RateLimit: kkconfig.Manager.MaxUploadRate,
				Policy:    policy,
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

			errMu.Lock()
			uploadedCount++
			errMu.Unlock()

			if !ui.IsQuiet() {
				switch {
				case res.Skipped:
					ui.PrintWarn(os.Stderr, "云端已存在同名文件，已跳过: %s/%s", targetDir, res.RemoteName)
				case res.Rapid:
					ui.PrintSuccess(os.Stderr, "云端秒传成功: %s/%s (%s)", targetDir, res.RemoteName, ui.FormatBytes(res.Size))
				default:
					ui.PrintSuccess(os.Stderr, "云端上传完成: %s/%s (%s)", targetDir, res.RemoteName, ui.FormatBytes(res.Size))
				}
			}
		}(df)
	}
	wg.Wait()
	if firstErr != nil {
		return uploadedCount, firstErr
	}
	return uploadedCount, nil
}

func cleanupLocalTorrentFiles(saveDir string, files []transfer.DownloadedTorrentFile) {
	baseClean := strings.ToLower(filepath.Clean(saveDir))
	dirs := make(map[string]struct{})
	for _, f := range files {
		if err := os.Remove(f.LocalPath); err == nil {
			dirs[filepath.Dir(f.LocalPath)] = struct{}{}
		}
	}

	var dirList []string
	for d := range dirs {
		for cur := d; strings.ToLower(filepath.Clean(cur)) != baseClean; cur = filepath.Dir(cur) {
			dirList = append(dirList, cur)
			parent := filepath.Dir(cur)
			if parent == cur {
				break
			}
		}
	}
	sort.Slice(dirList, func(i, j int) bool {
		return len(dirList[i]) > len(dirList[j])
	})
	seen := make(map[string]bool, len(dirList))
	for _, d := range dirList {
		key := strings.ToLower(filepath.Clean(d))
		if seen[key] || key == baseClean {
			continue
		}
		seen[key] = true
		_ = os.Remove(d)
	}
}

func renderBTDownloadResult(view BTResultView, elapsed time.Duration, mode ui.OutputMode) error {
	switch mode {
	case ui.OutputJSON:
		return ui.WriteJSON(os.Stdout, view)
	case ui.OutputPlain:
		rows := make([][]string, 0, len(view.Files))
		for _, f := range view.Files {
			rows = append(rows, []string{
				fmt.Sprintf("%d", f.Index),
				fmt.Sprintf("%d", f.Size),
				f.LocalPath,
			})
		}
		return ui.WriteTSV(os.Stdout, rows)
	default:
		ui.PrintSuccess(os.Stdout, "BT 下载完成: %s (共 %d 个文件, %s, 耗时 %s)",
			view.Name, len(view.Files), ui.FormatBytes(view.TotalSize), elapsed.Round(time.Millisecond))
		if !view.LocalDeleted {
			ui.PrintInfo(os.Stdout, "本地保存目录: %s", view.SaveDir)
		}
		if view.UploadedTo != "" {
			ui.PrintSuccess(os.Stdout, "已同步至夸克网盘: %s (共 %d 个文件, 本地文件已清理=%v)",
				view.UploadedTo, view.UploadedCount, view.LocalDeleted)
		}
		return nil
	}
}
