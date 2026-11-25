package kkcommand

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
)

// ShareSetOptions 控制创建分享链接参数
type ShareSetOptions struct {
	Passcode   string
	PeriodDays int
	Title      string
	FullFormat bool
}

// RunShareSet 创建夸克网盘文件或目录分享链接
func RunShareSet(targets []string, opt *ShareSetOptions, mode ui.OutputMode) error {
	if len(targets) == 0 {
		return fmt.Errorf("请指定待分享的文件或目录路径")
	}
	if opt == nil {
		opt = &ShareSetOptions{}
	}

	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fids := make([]string, 0, len(targets))
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		norm := resolveInputPath(t)
		if norm == "/" {
			return fmt.Errorf("安全限制：不支持直接分享根目录 /")
		}
		item, err := cli.Resolver().ResolvePath(ctx, norm)
		if err != nil {
			return err
		}
		fids = append(fids, item.FID)
		names = append(names, item.FileName)
	}

	title := strings.TrimSpace(opt.Title)
	if title == "" && len(names) > 0 {
		title = names[0]
		if len(names) > 1 {
			title = fmt.Sprintf("%s 等%d个文件", names[0], len(names))
		}
	}

	res, err := cli.CreateShare(ctx, quarkpan.CreateShareOptions{
		FIDs:       fids,
		Title:      title,
		Passcode:   opt.Passcode,
		PeriodDays: opt.PeriodDays,
	})
	if err != nil {
		return err
	}

	switch mode {
	case ui.OutputJSON:
		return ui.WriteJSON(os.Stdout, res)
	case ui.OutputPlain:
		return ui.WriteTSV(os.Stdout, [][]string{{
			res.ShareID, res.PWDID, res.ShareURL, res.Passcode, res.Title,
		}})
	default:
		passDisplay := res.Passcode
		if passDisplay == "" {
			passDisplay = "公开免提取码"
		}
		rows := [][]string{
			{"分享标题", res.Title},
			{"分享链接", res.ShareURL},
			{"提取码", passDisplay},
			{"有效期", formatShareExpiry(res.ExpiredType, res.ExpiredAt)},
			{"Share ID", res.ShareID},
		}
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Rows: rows,
		}))
		if opt.FullFormat {
			if res.Passcode != "" {
				fmt.Fprintf(os.Stdout, "我用夸克网盘分享了「%s」，点击链接即可保存。链接：%s 提取码：%s\n", res.Title, res.ShareURL, res.Passcode)
			} else {
				fmt.Fprintf(os.Stdout, "我用夸克网盘分享了「%s」，点击链接即可保存。链接：%s\n", res.Title, res.ShareURL)
			}
		}
		return nil
	}
}

// RunShareList 列出当前账号创建的分享链接
func RunShareList(page, size int, mode ui.OutputMode) error {
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	res, err := cli.ListMyShares(ctx, page, size)
	if err != nil {
		return err
	}

	switch mode {
	case ui.OutputJSON:
		return ui.WriteJSON(os.Stdout, res)
	case ui.OutputPlain:
		rows := make([][]string, 0, len(res.Items))
		for _, it := range res.Items {
			rows = append(rows, []string{
				it.ShareID,
				it.PWDID,
				it.Title,
				it.ShareURL,
				it.Passcode,
				formatShareExpiry(it.ExpiredType, it.ExpiredAt),
				strconv.FormatInt(it.ClickPV, 10),
				strconv.FormatInt(it.SavePV, 10),
			})
		}
		return ui.WriteTSV(os.Stdout, rows)
	default:
		if len(res.Items) == 0 {
			ui.PrintInfo(os.Stdout, "当前账号暂无分享记录")
			return nil
		}
		rows := make([][]string, 0, len(res.Items))
		for i, it := range res.Items {
			pass := it.Passcode
			if pass == "" {
				pass = "-"
			}
			rows = append(rows, []string{
				fmt.Sprintf("%02d", i+1),
				it.ShareID,
				it.Title,
				it.ShareURL,
				pass,
				formatShareExpiry(it.ExpiredType, it.ExpiredAt),
				fmt.Sprintf("%d/%d", it.ClickPV, it.SavePV),
			})
		}
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Headers:    []string{"#", "Share ID", "分享标题", "分享链接", "提取码", "有效期", "浏览/转存"},
			Rows:       rows,
			Alignments: []ui.TextAlign{ui.AlignRight, ui.AlignLeft, ui.AlignLeft, ui.AlignLeft, ui.AlignCenter, ui.AlignLeft, ui.AlignRight},
		}))
		return nil
	}
}

// RunShareCancel 批量取消指定 share_id 的分享链接
func RunShareCancel(shareIDs []string) error {
	if len(shareIDs) == 0 {
		return fmt.Errorf("请指定至少一个待取消的 Share ID")
	}
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := cli.CancelShares(ctx, shareIDs); err != nil {
		return err
	}
	ui.PrintSuccess(os.Stdout, "已取消 %d 条分享链接", len(shareIDs))
	return nil
}

func formatShareExpiry(expiredType int, expiredAt int64) string {
	if expiredAt > 0 {
		return formatTimestamp(expiredAt)
	}
	switch expiredType {
	case 1:
		return "永久有效"
	case 2:
		return "1 天"
	case 3:
		return "7 天"
	case 4:
		return "30 天"
	default:
		return "-"
	}
}
