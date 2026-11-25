package kkcommand

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/kkpan-cli/kkpan/internal/ui"
)

// RunRecycleList 分页列出夸克网盘回收站记录
func RunRecycleList(page, size int, mode ui.OutputMode) error {
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := cli.ListRecycle(ctx, page, size)
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
			rows = append(rows, []string{
				it.RecordID,
				it.FID,
				kind,
				strconv.FormatInt(it.Size, 10),
				formatTimestamp(it.DeletedAt),
				formatTimestamp(it.ExpiredAt),
				it.FileName,
			})
		}
		return ui.WriteTSV(os.Stdout, rows)
	default:
		if len(res.Items) == 0 {
			ui.PrintInfo(os.Stdout, "回收站为空")
			return nil
		}
		rows := make([][]string, 0, len(res.Items))
		for i, it := range res.Items {
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
				formatTimestamp(it.DeletedAt),
				it.RecordID,
			})
		}
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Headers:    []string{"#", "类型", "文件名", "大小", "删除时间", "Record ID"},
			Rows:       rows,
			Alignments: []ui.TextAlign{ui.AlignRight, ui.AlignCenter, ui.AlignLeft, ui.AlignRight, ui.AlignLeft, ui.AlignLeft},
		}))
		return nil
	}
}

// RunRecycleRestore 从回收站还原指定 record_id 的文件或目录
func RunRecycleRestore(recordIDs []string) error {
	if len(recordIDs) == 0 {
		return fmt.Errorf("请指定待还原的回收站 Record ID")
	}
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if err := cli.RestoreRecycle(ctx, recordIDs); err != nil {
		return err
	}
	ui.PrintSuccess(os.Stdout, "已从回收站还原 %d 项文件/目录", len(recordIDs))
	return nil
}

// RunRecycleDelete 彻底删除回收站指定记录，或当 clearAll=true 时清空回收站
func RunRecycleDelete(recordIDs []string, clearAll bool) error {
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	if clearAll {
		if err := cli.ClearRecycle(ctx); err != nil {
			return err
		}
		ui.PrintSuccess(os.Stdout, "已清空夸克网盘回收站")
		return nil
	}

	if len(recordIDs) == 0 {
		return fmt.Errorf("请指定待彻底删除的 Record ID，或使用 --all 清空回收站")
	}
	if err := cli.DeleteRecycle(ctx, recordIDs); err != nil {
		return err
	}
	ui.PrintSuccess(os.Stdout, "已从回收站彻底清除 %d 项记录", len(recordIDs))
	return nil
}
