package tui

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/kkpan-cli/kkpan/internal/kkconfig"
	"github.com/kkpan-cli/kkpan/internal/transfer"
	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
)

// DownloadJob 描述单个待下载文件的不可变调度作业
type DownloadJob struct {
	FID        string
	FileName   string
	Size       int64
	MD5        string
	UpdatedAt  int64
	RemotePath string
	DestPath   string
}

// UploadJob 描述单个待上传文件的不可变调度作业
type UploadJob struct {
	LocalPath string
	FileName  string
	Size      int64
	RemoteDir string
}

// CloudBackend 抽象 TUI 所需的网盘操作，实现全屏状态机与真实协议层的彻底解耦
type CloudBackend interface {
	ListDir(dir string, opt *quarkpan.ListOptions) ([]quarkpan.FileItem, error)
	Mkdir(fullPath string) error
	Rename(fid, newName string) error
	Remove(fids []string) error
	Quota() (*quarkpan.MemberQuota, error)
	PrepareDownloads(ctx context.Context, targets []quarkpan.FileItem, baseCWD string) ([]DownloadJob, error)
	ExecuteDownload(ctx context.Context, job DownloadJob, onProgress func(transfer.ProgressSnapshot)) (*transfer.DownloadResult, error)
	PrepareUploads(ctx context.Context, localPaths []string, remoteDir string) ([]UploadJob, error)
	ExecuteUpload(ctx context.Context, job UploadJob, onProgress func(transfer.ProgressSnapshot)) (*transfer.UploadResult, error)
	ConcurrencyLimits() (downloadLoad, uploadLoad int)
	SaveDownloadLoad(load int)
	SaveWorkdir(dir string)
	SaveTheme(theme ui.ThemeName)
	UserSummary() (nickname, uid, memberType, saveDir string, theme ui.ThemeName, loggedIn bool)
}

type quarkBackend struct{}

// NewQuarkBackend 创建基于当前活跃夸克账号的真实网盘后端
func NewQuarkBackend() CloudBackend {
	return &quarkBackend{}
}

func (b *quarkBackend) client() (*quarkpan.Client, error) {
	cli, err := kkconfig.Manager.ActiveClient()
	if err != nil {
		return nil, err
	}
	if !cli.HasCookie() {
		return nil, quarkpan.ErrNotLoggedIn
	}
	return cli, nil
}

func (b *quarkBackend) ListDir(dir string, opt *quarkpan.ListOptions) ([]quarkpan.FileItem, error) {
	cli, err := b.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return cli.ListDir(ctx, dir, opt)
}

func (b *quarkBackend) Mkdir(fullPath string) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err = cli.MkdirAll(ctx, fullPath)
	return err
}

func (b *quarkBackend) Rename(fid, newName string) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return cli.Rename(ctx, fid, newName)
}

func (b *quarkBackend) Remove(fids []string) error {
	cli, err := b.client()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return cli.Delete(ctx, fids)
}

func (b *quarkBackend) Quota() (*quarkpan.MemberQuota, error) {
	cli, err := b.client()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return cli.GetMemberQuota(ctx)
}

func (b *quarkBackend) ConcurrencyLimits() (int, int) {
	dl := kkconfig.Manager.MaxDownloadLoad
	if dl <= 0 {
		dl = 3
	}
	ul := kkconfig.Manager.MaxUploadLoad
	if ul <= 0 {
		ul = 2
	}
	return dl, ul
}

func (b *quarkBackend) SaveDownloadLoad(load int) {
	if load <= 0 {
		return
	}
	kkconfig.Manager.MaxDownloadLoad = load
	_ = kkconfig.Manager.Save()
}

// PrepareDownloads 递归展开选中的文件或目录，生成静默下载作业列表（绝不向终端打印任何输出）
func (b *quarkBackend) PrepareDownloads(ctx context.Context, targets []quarkpan.FileItem, baseCWD string) ([]DownloadJob, error) {
	if len(targets) == 0 {
		return nil, fmt.Errorf("未指定待下载的文件或目录")
	}
	cli, err := b.client()
	if err != nil {
		return nil, err
	}
	saveDir := strings.TrimSpace(kkconfig.Manager.SaveDir)
	if saveDir == "" {
		saveDir = "."
	}
	cleanCWD := quarkpan.NormalizePath("/", baseCWD)

	var jobs []DownloadJob
	for _, it := range targets {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if it.FullPath == "" {
			it.FullPath = path.Join(cleanCWD, it.FileName)
		}
		if err := collectDownloadJobs(ctx, cli, it, saveDir, &jobs); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

func collectDownloadJobs(ctx context.Context, cli *quarkpan.Client, item quarkpan.FileItem, baseDir string, out *[]DownloadJob) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !item.IsDir {
		*out = append(*out, DownloadJob{
			FID:        item.FID,
			FileName:   item.FileName,
			Size:       item.Size,
			MD5:        item.MD5,
			UpdatedAt:  item.UpdatedAt,
			RemotePath: item.FullPath,
			DestPath:   filepath.Join(baseDir, item.FileName),
		})
		return nil
	}

	nextBase := baseDir
	if item.FileName != "" && item.FileName != "/" {
		nextBase = filepath.Join(baseDir, item.FileName)
	}
	children, err := cli.ListDirByFID(ctx, item.FID, nil)
	if err != nil {
		return err
	}
	for _, ch := range children {
		if ch.FullPath == "" && item.FullPath != "" {
			ch.FullPath = path.Join(item.FullPath, ch.FileName)
		}
		if err := collectDownloadJobs(ctx, cli, ch, nextBase, out); err != nil {
			return err
		}
	}
	return nil
}

// ExecuteDownload 在后台执行单个下载作业，并通过回调推送实时进度
func (b *quarkBackend) ExecuteDownload(ctx context.Context, job DownloadJob, onProgress func(transfer.ProgressSnapshot)) (*transfer.DownloadResult, error) {
	cli, err := b.client()
	if err != nil {
		return nil, err
	}
	links, err := cli.GetDownloadLinks(ctx, []string{job.FID})
	if err != nil {
		return nil, err
	}
	if len(links) == 0 {
		return nil, fmt.Errorf("未获取到文件 %s 的下载直链", job.FileName)
	}
	link := links[0]
	if link.MD5 == "" {
		link.MD5 = job.MD5
	}
	if link.UpdatedAt == 0 {
		link.UpdatedAt = job.UpdatedAt
	}

	parallel := kkconfig.Manager.MaxParallel
	if parallel <= 0 {
		parallel = 1
	}
	dl := transfer.NewDownloader(cli)
	return dl.DownloadFile(ctx, link, job.DestPath, transfer.DownloadConfig{
		Parallel:    parallel,
		MaxRetry:    3,
		RateLimit:   kkconfig.Manager.MaxDownloadRate,
		NoCheck:     kkconfig.Manager.NoCheck,
		ModifyMTime: kkconfig.Manager.ModifyMTime,
		OnProgress:  onProgress,
	})
}

// PrepareUploads 递归展开本地文件或目录，生成待上传作业列表（绝不向终端打印任何输出）
func (b *quarkBackend) PrepareUploads(ctx context.Context, localPaths []string, remoteDir string) ([]UploadJob, error) {
	if len(localPaths) == 0 {
		return nil, fmt.Errorf("请指定至少一个本地文件或目录")
	}
	normRemote := quarkpan.NormalizePath("/", remoteDir)

	var jobs []UploadJob
	for _, lp := range localPaths {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		absLocal, err := filepath.Abs(strings.TrimSpace(lp))
		if err != nil {
			return nil, err
		}
		stat, err := os.Stat(absLocal)
		if err != nil {
			return nil, fmt.Errorf("读取本地路径失败: %w", err)
		}
		if !stat.IsDir() {
			jobs = append(jobs, UploadJob{
				LocalPath: absLocal,
				FileName:  stat.Name(),
				Size:      stat.Size(),
				RemoteDir: normRemote,
			})
			continue
		}

		baseRoot := filepath.Dir(absLocal)
		if err := filepath.WalkDir(absLocal, func(p string, d os.DirEntry, walkErr error) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() {
				return nil
			}
			info, infoErr := d.Info()
			if infoErr != nil {
				return infoErr
			}
			rel, err := filepath.Rel(baseRoot, filepath.Dir(p))
			if err != nil {
				return err
			}
			dstDir := normRemote
			if rel != "." {
				dstDir = path.Join(normRemote, filepath.ToSlash(rel))
			}
			jobs = append(jobs, UploadJob{
				LocalPath: p,
				FileName:  info.Name(),
				Size:      info.Size(),
				RemoteDir: dstDir,
			})
			return nil
		}); err != nil {
			return nil, err
		}
	}
	return jobs, nil
}

// ExecuteUpload 在后台执行单个上传作业，并通过回调推送实时进度
func (b *quarkBackend) ExecuteUpload(ctx context.Context, job UploadJob, onProgress func(transfer.ProgressSnapshot)) (*transfer.UploadResult, error) {
	cli, err := b.client()
	if err != nil {
		return nil, err
	}
	pdirFID, err := cli.MkdirAll(ctx, job.RemoteDir)
	if err != nil {
		return nil, err
	}
	parallel := kkconfig.Manager.MaxUploadParallel
	if parallel <= 0 {
		parallel = 2
	}
	policy := strings.TrimSpace(kkconfig.Manager.UPolicy)
	if policy == "" {
		policy = "skip"
	}
	ul := transfer.NewUploader(cli)
	return ul.UploadFile(ctx, job.LocalPath, pdirFID, transfer.UploadConfig{
		Parallel:   parallel,
		MaxRetry:   3,
		RateLimit:  kkconfig.Manager.MaxUploadRate,
		Policy:     policy,
		OnProgress: onProgress,
	})
}

func (b *quarkBackend) SaveWorkdir(dir string) {
	_ = kkconfig.Manager.UpdateWorkdir(dir)
}

func (b *quarkBackend) SaveTheme(theme ui.ThemeName) {
	if string(theme) == "" {
		return
	}
	kkconfig.Manager.Theme = string(theme)
	_ = kkconfig.Manager.Save()
}

func (b *quarkBackend) UserSummary() (string, string, string, string, ui.ThemeName, bool) {
	acc := kkconfig.Manager.ActiveAccount()
	theme := ui.EffectiveTheme(kkconfig.Manager.Theme).Name
	if acc == nil || acc.Nickname == "" {
		return "", "", "", kkconfig.Manager.SaveDir, theme, false
	}
	return acc.Nickname, acc.UID, acc.MemberType, kkconfig.Manager.SaveDir, theme, true
}

// Run 启动全屏日式二次元档案浏览器，退出时优雅停止后台传输并同步工作目录与所选主题
func Run(startDir string) error {
	if !ui.IsTTY(os.Stdout) || !ui.IsTTY(os.Stdin) {
		return fmt.Errorf("'kkpan ui' 需要交互式终端 (TTY)，脚本或管道请使用 'kkpan ls --json'")
	}
	backend := NewQuarkBackend()
	_, _, _, _, _, loggedIn := backend.UserSummary()
	if !loggedIn {
		return fmt.Errorf("当前尚未登录夸克网盘，请先执行 'kkpan login -qr'")
	}

	acc := kkconfig.Manager.ActiveAccount()
	if startDir == "" {
		if acc != nil && acc.Workdir != "" {
			startDir = acc.Workdir
		} else {
			startDir = "/"
		}
	} else {
		wd := "/"
		if acc != nil && acc.Workdir != "" {
			wd = acc.Workdir
		}
		startDir = quarkpan.NormalizePath(wd, startDir)
	}

	m := NewModel(backend, startDir)
	defer m.Close()

	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return err
	}
	if fm, ok := finalModel.(*Model); ok {
		fm.Close()
		if fm.cwd != "" {
			backend.SaveWorkdir(fm.cwd)
		}
		if fm.themeChanged {
			backend.SaveTheme(fm.theme)
		}
	}
	return nil
}
