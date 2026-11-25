package kkcommand

import (
	"context"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/kkpan-cli/kkpan/internal/kkconfig"
	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
)

// LsOptions 控制 ls 命令的排序与列展示选项
type LsOptions struct {
	ShowFID   bool
	SortBy    string
	Desc      bool
	Refresh   bool
}

func activeWorkdir() string {
	if acc := kkconfig.Manager.ActiveAccount(); acc != nil && acc.Workdir != "" {
		return acc.Workdir
	}
	return "/"
}

func resolveInputPath(raw string) string {
	return quarkpan.NormalizePath(activeWorkdir(), raw)
}

func formatTimestamp(ts int64) string {
	if ts <= 0 {
		return "-"
	}
	if ts > 1e12 {
		ts = ts / 1000
	}
	return time.Unix(ts, 0).Format("2006-01-02 15:04")
}

// RunLs 列举网盘目录并按输出模式渲染
func RunLs(targets []string, opt LsOptions, mode ui.OutputMode) error {
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if len(targets) == 0 {
		targets = []string{activeWorkdir()}
	}

	sortOrder := "asc"
	if opt.Desc {
		sortOrder = "desc"
	}
	listOpt := &quarkpan.ListOptions{
		SortField:    opt.SortBy,
		SortOrder:    sortOrder,
		ForceRefresh: opt.Refresh,
	}

	var combined []quarkpan.FileItem
	for _, t := range targets {
		norm := resolveInputPath(t)
		items, err := cli.ListDir(ctx, norm, listOpt)
		if err != nil {
			return err
		}
		combined = append(combined, items...)
	}

	switch mode {
	case ui.OutputJSON:
		if combined == nil {
			combined = []quarkpan.FileItem{}
		}
		return ui.WriteJSON(os.Stdout, combined)
	case ui.OutputPlain:
		rows := make([][]string, 0, len(combined))
		for _, it := range combined {
			kind := "F"
			if it.IsDir {
				kind = "D"
			}
			rows = append(rows, []string{kind, it.FID, strconv.FormatInt(it.Size, 10), formatTimestamp(it.UpdatedAt), it.FileName})
		}
		return ui.WriteTSV(os.Stdout, rows)
	default:
		if len(combined) == 0 {
			ui.PrintInfo(os.Stdout, "目录为空")
			return nil
		}
		headers := []string{"#", "类型", "文件名", "大小", "修改时间"}
		aligns := []ui.TextAlign{ui.AlignRight, ui.AlignCenter, ui.AlignLeft, ui.AlignRight, ui.AlignLeft}
		if opt.ShowFID {
			headers = append(headers, "FID")
			aligns = append(aligns, ui.AlignLeft)
		}
		rows := make([][]string, 0, len(combined))
		for i, it := range combined {
			kind := "文件"
			sizeStr := ui.FormatBytes(it.Size)
			nameStr := it.FileName
			if it.IsDir {
				kind = "目录"
				sizeStr = "-"
				nameStr += "/"
			}
			row := []string{
				fmt.Sprintf("%02d", i+1),
				kind,
				nameStr,
				sizeStr,
				formatTimestamp(it.UpdatedAt),
			}
			if opt.ShowFID {
				row = append(row, it.FID)
			}
			rows = append(rows, row)
		}
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Headers:    headers,
			Rows:       rows,
			Alignments: aligns,
		}))
		return nil
	}
}

// RunCd 切换当前活跃账号的工作目录并验证目录存在
func RunCd(target string) error {
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	if strings.TrimSpace(target) == "" {
		target = "/"
	}
	norm := resolveInputPath(target)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, resolvedPath, err := cli.Resolver().ResolveDirFID(ctx, norm)
	if err != nil {
		return err
	}
	if err := kkconfig.Manager.UpdateWorkdir(resolvedPath); err != nil {
		return err
	}
	ui.PrintSuccess(os.Stdout, "工作目录已切换至: %s", resolvedPath)
	return nil
}

// RunPwd 打印当前网盘工作目录
func RunPwd() error {
	fmt.Fprintln(os.Stdout, activeWorkdir())
	return nil
}

// RunTree 以树形结构展示网盘目录层级
func RunTree(target string, depth int, showFID bool) error {
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	if depth <= 0 {
		depth = 2
	}
	norm := resolveInputPath(target)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	dirFID, displayPath, err := cli.Resolver().ResolveDirFID(ctx, norm)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, displayPath)
	return printTreeRecursive(ctx, cli, dirFID, "", 1, depth, showFID)
}

func printTreeRecursive(ctx context.Context, cli *quarkpan.Client, pdirFID, prefix string, curDepth, maxDepth int, showFID bool) error {
	items, err := cli.ListDirByFID(ctx, pdirFID, nil)
	if err != nil {
		return err
	}
	ascii := ui.ASCIIOnly()
	branchMid, branchLast := "├── ", "└── "
	indentMid, indentLast := "│   ", "    "
	if ascii {
		branchMid, branchLast = "|-- ", "`-- "
		indentMid, indentLast = "|   ", "    "
	}

	for i, it := range items {
		isLast := i == len(items)-1
		conn := branchMid
		nextPrefix := prefix + indentMid
		if isLast {
			conn = branchLast
			nextPrefix = prefix + indentLast
		}
		name := it.FileName
		if it.IsDir {
			name += "/"
		}
		if showFID {
			fmt.Fprintf(os.Stdout, "%s%s%s  [%s]\n", prefix, conn, name, it.FID)
		} else {
			fmt.Fprintf(os.Stdout, "%s%s%s\n", prefix, conn, name)
		}
		if it.IsDir && curDepth < maxDepth {
			if err := printTreeRecursive(ctx, cli, it.FID, nextPrefix, curDepth+1, maxDepth, showFID); err != nil {
				return err
			}
		}
	}
	return nil
}

// RunSearch 全盘搜索文件或目录
func RunSearch(keyword string, page, size int, mode ui.OutputMode) error {
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := cli.Search(ctx, keyword, page, size)
	if err != nil {
		return err
	}

	switch mode {
	case ui.OutputJSON:
		return ui.WriteJSON(os.Stdout, res)
	case ui.OutputPlain:
		rows := make([][]string, 0, len(res.Items))
		for _, it := range res.Items {
			kind := "F"
			if it.IsDir {
				kind = "D"
			}
			rows = append(rows, []string{kind, it.FID, strconv.FormatInt(it.Size, 10), it.FileName})
		}
		return ui.WriteTSV(os.Stdout, rows)
	default:
		if len(res.Items) == 0 {
			ui.PrintInfo(os.Stdout, "未搜索到匹配 %q 的结果", keyword)
			return nil
		}
		rows := make([][]string, 0, len(res.Items))
		for i, it := range res.Items {
			kind := "文件"
			sz := ui.FormatBytes(it.Size)
			if it.IsDir {
				kind = "目录"
				sz = "-"
			}
			rows = append(rows, []string{fmt.Sprintf("%02d", i+1), kind, it.FileName, sz, it.FID})
		}
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Headers:    []string{"#", "类型", "文件名", "大小", "FID"},
			Rows:       rows,
			Alignments: []ui.TextAlign{ui.AlignRight, ui.AlignCenter, ui.AlignLeft, ui.AlignRight, ui.AlignLeft},
		}))
		return nil
	}
}

// RunMeta 查看文件或目录详细元信息
func RunMeta(target string, mode ui.OutputMode) error {
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	norm := resolveInputPath(target)
	item, err := cli.Resolver().ResolvePath(ctx, norm)
	if err != nil {
		return err
	}

	switch mode {
	case ui.OutputJSON:
		return ui.WriteJSON(os.Stdout, item)
	case ui.OutputPlain:
		return ui.WriteTSV(os.Stdout, [][]string{{
			item.FID, item.PDirFID, item.FileName, strconv.FormatInt(item.Size, 10), item.MD5, formatTimestamp(item.UpdatedAt),
		}})
	default:
		kind := "文件"
		if item.IsDir {
			kind = "目录"
		}
		md5Val := item.MD5
		if md5Val == "" {
			md5Val = "-"
		}
		rows := [][]string{
			{"名称", item.FileName},
			{"类型", kind},
			{"FID", item.FID},
			{"父目录 FID", item.PDirFID},
			{"体积", fmt.Sprintf("%s (%d B)", ui.FormatBytes(item.Size), item.Size)},
			{"MD5", md5Val},
			{"修改时间", formatTimestamp(item.UpdatedAt)},
			{"网盘路径", item.FullPath},
		}
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Rows: rows,
		}))
		return nil
	}
}

// RunMkdir 创建网盘目录（默认递归创建父路径）
func RunMkdir(targets []string) error {
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, t := range targets {
		norm := resolveInputPath(t)
		fid, err := cli.MkdirAll(ctx, norm)
		if err != nil {
			return err
		}
		ui.PrintSuccess(os.Stdout, "已创建目录 %s (FID: %s)", norm, fid)
	}
	return nil
}

// RunRename 重命名网盘文件或目录
func RunRename(target, newName string) error {
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	norm := resolveInputPath(target)
	item, err := cli.Resolver().ResolvePath(ctx, norm)
	if err != nil {
		return err
	}
	if err := cli.Rename(ctx, item.FID, newName); err != nil {
		return err
	}
	ui.PrintSuccess(os.Stdout, "已将 %s 重命名为 %s", item.FileName, newName)
	return nil
}

// RunMv 移动一个或多个文件/目录到目标目录
func RunMv(args []string) error {
	return moveOrCopy(args, true)
}

// RunCp 复制一个或多个文件/目录到目标目录
func RunCp(args []string) error {
	return moveOrCopy(args, false)
}

func moveOrCopy(args []string, isMove bool) error {
	if len(args) < 2 {
		return fmt.Errorf("参数不足：至少需要一个源路径和一个目标目录")
	}
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	dstRaw := args[len(args)-1]
	srcRaw := args[:len(args)-1]

	toPDirFID, dstDisplay, err := cli.Resolver().ResolveDirFID(ctx, resolveInputPath(dstRaw))
	if err != nil {
		return err
	}

	fids := make([]string, 0, len(srcRaw))
	for _, s := range srcRaw {
		it, err := cli.Resolver().ResolvePath(ctx, resolveInputPath(s))
		if err != nil {
			return err
		}
		fids = append(fids, it.FID)
	}

	if isMove {
		if err := cli.Move(ctx, fids, toPDirFID); err != nil {
			return err
		}
		ui.PrintSuccess(os.Stdout, "已将 %d 项移动至 %s", len(fids), dstDisplay)
		return nil
	}
	if err := cli.Copy(ctx, fids, toPDirFID); err != nil {
		return err
	}
	ui.PrintSuccess(os.Stdout, "已将 %d 项复制至 %s", len(fids), dstDisplay)
	return nil
}

// RunRm 删除一个或多个文件/目录（移入夸克网盘回收站）
func RunRm(targets []string) error {
	if len(targets) == 0 {
		return fmt.Errorf("请指定待删除的文件或目录路径")
	}
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	fids := make([]string, 0, len(targets))
	for _, t := range targets {
		norm := resolveInputPath(t)
		if norm == "/" {
			return fmt.Errorf("安全限制：不允许删除根目录 /")
		}
		it, err := cli.Resolver().ResolvePath(ctx, norm)
		if err != nil {
			return err
		}
		fids = append(fids, it.FID)
	}
	if err := cli.Delete(ctx, fids); err != nil {
		return err
	}
	ui.PrintSuccess(os.Stdout, "已移入回收站 (%d 项)", len(fids))
	return nil
}

// resolveChildPath 安全拼接网盘展示路径
func resolveChildPath(parentPath, name string) string {
	if strings.HasPrefix(parentPath, quarkpan.FIDPrefix) {
		return parentPath + "/" + name
	}
	return path.Join(parentPath, name)
}
