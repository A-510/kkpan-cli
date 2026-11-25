package kkcommand

import (
	"context"
	"fmt"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
)

// TransferOptions 控制分享转存命令行为
type TransferOptions struct {
	ToDir     string
	Collect   bool
	Download  bool
	ListOnly  bool
	FIDFilter string
	PDirFID   string
}

// ShareTransferView 描述转存命令的结构化输出摘要
type ShareTransferView struct {
	PWDID        string   `json:"pwd_id"`
	Title        string   `json:"title"`
	TargetDir    string   `json:"target_dir"`
	ToPDirFID    string   `json:"to_pdir_fid"`
	SavedCount   int      `json:"saved_count"`
	SavedTopFIDs []string `json:"saved_top_fids,omitempty"`
}

// RunShareTransfer 解析夸克分享链接或口令，支持仅查看列表、转存到网盘及转存后自动下载
func RunShareTransfer(args []string, opt *TransferOptions, mode ui.OutputMode) error {
	if opt == nil {
		opt = &TransferOptions{}
	}
	pwdID, passcode, err := quarkpan.ParseShareInput(args)
	if err != nil {
		return err
	}

	cli, err := requireActiveClient()
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	tokenInfo, err := cli.GetShareToken(ctx, pwdID, passcode)
	if err != nil {
		return err
	}

	pdirFID := strings.TrimSpace(opt.PDirFID)
	if pdirFID == "" {
		pdirFID = quarkpan.RootFID
	}
	detail, err := cli.ListShareFiles(ctx, pwdID, tokenInfo.SToken, pdirFID)
	if err != nil {
		return err
	}

	shareTitle := strings.TrimSpace(tokenInfo.Title)
	if detail.Share != nil && strings.TrimSpace(detail.Share.Title) != "" {
		shareTitle = strings.TrimSpace(detail.Share.Title)
	}

	if opt.ListOnly {
		return renderShareFiles(detail, mode)
	}

	selected, err := filterShareItems(detail.Items, opt.FIDFilter)
	if err != nil {
		return err
	}
	if len(selected) == 0 {
		ui.PrintInfo(os.Stdout, "该分享目录下没有可转存的文件或目录")
		return nil
	}

	targetDir := activeWorkdir()
	if strings.TrimSpace(opt.ToDir) != "" {
		targetDir = resolveInputPath(opt.ToDir)
	}
	toPDirFID, err := cli.MkdirAll(ctx, targetDir)
	if err != nil {
		return err
	}

	if opt.Collect {
		folderName := sanitizeShareFolderName(shareTitle)
		if folderName == "" {
			folderName = "share_" + pwdID
		}
		targetDir = resolveChildPath(targetDir, folderName)
		toPDirFID, err = cli.MkdirAll(ctx, targetDir)
		if err != nil {
			return err
		}
	}

	fids := make([]string, 0, len(selected))
	fidTokens := make([]string, 0, len(selected))
	for _, it := range selected {
		fids = append(fids, it.FID)
		fidTokens = append(fidTokens, it.ShareFIDToken)
	}

	saveRes, err := cli.SaveShareFiles(ctx, quarkpan.ShareSaveRequest{
		PWDID:     pwdID,
		SToken:    tokenInfo.SToken,
		PDirFID:   pdirFID,
		ToPDirFID: toPDirFID,
		FIDs:      fids,
		FIDTokens: fidTokens,
	})
	if err != nil {
		return err
	}

	view := ShareTransferView{
		PWDID:        pwdID,
		Title:        shareTitle,
		TargetDir:    targetDir,
		ToPDirFID:    toPDirFID,
		SavedCount:   len(selected),
		SavedTopFIDs: saveRes.SavedTopFIDs,
	}

	switch mode {
	case ui.OutputJSON:
		if err := ui.WriteJSON(os.Stdout, view); err != nil {
			return err
		}
	case ui.OutputPlain:
		if err := ui.WriteTSV(os.Stdout, [][]string{{
			view.PWDID,
			view.Title,
			view.TargetDir,
			view.ToPDirFID,
			strconv.Itoa(view.SavedCount),
			strings.Join(view.SavedTopFIDs, ","),
		}}); err != nil {
			return err
		}
	default:
		displayTitle := view.Title
		if displayTitle == "" {
			displayTitle = pwdID
		}
		ui.PrintSuccess(os.Stdout, "转存成功！[%s] 共 %d 项已保存至 %s", displayTitle, view.SavedCount, view.TargetDir)
	}

	if opt.Download {
		dlTargets := make([]string, 0, len(selected))
		if len(saveRes.SavedTopFIDs) > 0 {
			for _, fid := range saveRes.SavedTopFIDs {
				if strings.TrimSpace(fid) != "" {
					dlTargets = append(dlTargets, quarkpan.FIDPrefix+strings.TrimSpace(fid))
				}
			}
		}
		if len(dlTargets) == 0 {
			for _, it := range selected {
				dlTargets = append(dlTargets, resolveChildPath(targetDir, it.FileName))
			}
		}
		return RunDownload(dlTargets, &DownloadOptions{})
	}
	return nil
}

func renderShareFiles(detail *quarkpan.ShareDetailResult, mode ui.OutputMode) error {
	switch mode {
	case ui.OutputJSON:
		return ui.WriteJSON(os.Stdout, detail)
	case ui.OutputPlain:
		rows := make([][]string, 0, len(detail.Items))
		for _, it := range detail.Items {
			kind := "F"
			if it.IsDir {
				kind = "D"
			}
			rows = append(rows, []string{kind, it.FID, strconv.FormatInt(it.Size, 10), formatTimestamp(it.UpdatedAt), it.FileName})
		}
		return ui.WriteTSV(os.Stdout, rows)
	default:
		if detail.Share != nil && strings.TrimSpace(detail.Share.Title) != "" {
			ui.PrintInfo(os.Stdout, "分享标题: %s", detail.Share.Title)
		}
		if len(detail.Items) == 0 {
			ui.PrintInfo(os.Stdout, "该分享目录下暂无文件")
			return nil
		}
		rows := make([][]string, 0, len(detail.Items))
		for i, it := range detail.Items {
			kind := "文件"
			sizeStr := ui.FormatBytes(it.Size)
			nameStr := it.FileName
			if it.IsDir {
				kind = "目录"
				sizeStr = "-"
				nameStr += "/"
			}
			rows = append(rows, []string{
				fmt.Sprintf("%02d", i+1),
				kind,
				nameStr,
				sizeStr,
				it.FID,
			})
		}
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Headers:    []string{"#", "类型", "文件名", "大小", "FID"},
			Rows:       rows,
			Alignments: []ui.TextAlign{ui.AlignRight, ui.AlignCenter, ui.AlignLeft, ui.AlignRight, ui.AlignLeft},
		}))
		return nil
	}
}

func filterShareItems(items []quarkpan.ShareFileItem, fidFilter string) ([]quarkpan.ShareFileItem, error) {
	raw := strings.TrimSpace(fidFilter)
	if raw == "" {
		return items, nil
	}
	wanted := make(map[string]bool)
	var ordered []string
	for _, seg := range strings.Split(raw, ",") {
		id := strings.TrimSpace(seg)
		if id != "" && !wanted[id] {
			wanted[id] = true
			ordered = append(ordered, id)
		}
	}
	if len(wanted) == 0 {
		return items, nil
	}

	matched := make([]quarkpan.ShareFileItem, 0, len(wanted))
	found := make(map[string]bool, len(wanted))
	for _, it := range items {
		if wanted[it.FID] {
			matched = append(matched, it)
			found[it.FID] = true
		}
	}
	for _, id := range ordered {
		if !found[id] {
			return nil, fmt.Errorf("分享当前目录下未找到指定的 FID: %s", id)
		}
	}
	return matched, nil
}

func sanitizeShareFolderName(raw string) string {
	cleaned := strings.TrimSpace(raw)
	if cleaned == "" {
		return ""
	}
	replacer := strings.NewReplacer(
		"/", "_",
		`\`, "_",
		":", "_",
		"*", "_",
		"?", "_",
		`"`, "_",
		"<", "_",
		">", "_",
		"|", "_",
	)
	cleaned = strings.TrimSpace(replacer.Replace(cleaned))
	cleaned = strings.Trim(path.Base("/"+cleaned), ".")
	if cleaned == "" || cleaned == "/" {
		return ""
	}
	return cleaned
}
