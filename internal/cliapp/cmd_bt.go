package cliapp

import (
	"fmt"
	"path/filepath"

	"github.com/kkpan-cli/kkpan/internal/kkcommand"
	"github.com/urfave/cli/v2"
)

func btCommands(app *cli.App) []*cli.Command {
	return []*cli.Command{
		{
			Name:      "bt",
			Aliases:   []string{"torrent", "magnet", "offlinedl"},
			Usage:     "解析并下载 BT 种子 (.torrent) 或磁力链接 (支持选文件与自动上传网盘)",
			ArgsUsage: "<种子路径 / 磁力链接 / InfoHash / 网盘.torrent>",
			Category:  "上传与下载",
			Before:    reloadBefore,
			Description: `在本地识别 .torrent 种子文件、magnet:? 磁力链接、裸 InfoHash、远程种子 URL 或夸克网盘内的 .torrent 文件，
支持列出内部文件清单、按序号/关键词选择性下载，并在完成后自动上传至夸克网盘。

示例:
  kkpan bt --list ./movie.torrent
  kkpan bt --list "magnet:?xt=urn:btih:..."
  kkpan bt --files 1,3-5 ./pack.torrent
  kkpan bt --match "*.mp4,*.srt" --saveto ./videos "magnet:?xt=urn:btih:..."
  kkpan bt --all --upload /离线资源 --delete-local ./demo.torrent`,
			Flags: append([]cli.Flag{
				&cli.BoolFlag{Name: "list", Aliases: []string{"L"}, Usage: "仅解析并列出种子/磁力内部文件清单，不下载"},
				&cli.StringFlag{Name: "files", Aliases: []string{"f"}, Usage: "按 1-based 序号筛选下载文件（如 1,3,5-8）"},
				&cli.StringFlag{Name: "match", Aliases: []string{"m"}, Usage: "按关键词或通配符筛选下载文件（如 *.mp4,*.mkv）"},
				&cli.BoolFlag{Name: "all", Usage: "多文件种子跳过交互选择，直接全部下载"},
				&cli.StringFlag{Name: "saveto", Usage: "指定本地保存目录（默认读取配置 savedir）"},
				&cli.BoolFlag{Name: "save", Usage: "保存到当前本地工作目录"},
				&cli.StringFlag{Name: "upload", Aliases: []string{"to"}, Usage: "下载完成后自动上传至指定夸克网盘目录"},
				&cli.BoolFlag{Name: "delete-local", Usage: "配合 --upload 使用，上传成功后删除本地下载文件"},
				&cli.StringFlag{Name: "rate", Usage: "限制 BT 下载速率（如 10MB/s、512KB/s）"},
				&cli.IntFlag{Name: "timeout", Value: 90, Usage: "磁力链接元数据解析超时秒数"},
				&cli.StringFlag{Name: "tracker", Usage: "额外追加 Tracker URL（多个以逗号分隔）"},
				&cli.BoolFlag{Name: "no-default-trackers", Usage: "禁用内置公共兜底 Tracker"},
				&cli.IntFlag{Name: "port", Value: 0, Usage: "指定 BT P2P 监听端口（默认 0 随机端口）"},
			}, outputFlags()...),
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 {
					return fmt.Errorf("请提供 .torrent 种子文件路径、磁力链接或 InfoHash")
				}
				saveTo := ""
				if c.Bool("save") {
					saveTo = "."
				} else if c.String("saveto") != "" {
					saveTo = filepath.Clean(c.String("saveto"))
				}
				return kkcommand.RunBT(c.Args().Slice(), &kkcommand.BTOptions{
					SaveTo:             saveTo,
					ListOnly:           c.Bool("list"),
					FilesExpr:          c.String("files"),
					MatchExpr:          c.String("match"),
					All:                c.Bool("all"),
					UploadTo:           c.String("upload"),
					DeleteLocal:        c.Bool("delete-local"),
					RateLimitRaw:       c.String("rate"),
					MetadataTimeoutSec: c.Int("timeout"),
					TrackersRaw:        c.String("tracker"),
					NoDefaultTrackers:  c.Bool("no-default-trackers"),
					ListenPort:         c.Int("port"),
				}, outputMode(c))
			},
		},
	}
}
