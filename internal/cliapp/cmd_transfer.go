package cliapp

import (
	"fmt"
	"path/filepath"

	"github.com/kkpan-cli/kkpan/internal/kkcommand"
	"github.com/urfave/cli/v2"
)

func transferCommands(app *cli.App) []*cli.Command {
	return []*cli.Command{
		{
			Name:      "locate",
			Usage:     "提取文件临时下载直链",
			ArgsUsage: "<文件路径...>",
			Category:  "上传与下载",
			Before:    reloadBefore,
			Flags:     outputFlags(),
			Action: func(c *cli.Context) error {
				return kkcommand.RunLocate(c.Args().Slice(), outputMode(c))
			},
		},
		{
			Name:      "download",
			Aliases:   []string{"d"},
			Usage:     "多线程分片并发下载文件或目录 (支持断点续传)",
			ArgsUsage: "<网盘路径或 fid:<id>...>",
			Category:  "上传与下载",
			Before:    reloadBefore,
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "saveto", Usage: "指定本地保存目录"},
				&cli.BoolFlag{Name: "save", Usage: "保存到当前本地工作目录"},
				&cli.IntFlag{Name: "p", Usage: "单文件分片下载线程数"},
				&cli.IntFlag{Name: "l", Usage: "同时下载的文件并发数"},
				&cli.IntFlag{Name: "retry", Value: 3, Usage: "分片失败最大重试次数"},
				&cli.BoolFlag{Name: "ow", Usage: "覆盖本地已存在同名文件"},
				&cli.BoolFlag{Name: "nocheck", Usage: "跳过下载完成后 MD5 校验"},
				&cli.BoolFlag{Name: "mtime", Usage: "同步服务端文件修改时间"},
				&cli.BoolFlag{Name: "fullpath", Usage: "按网盘完整目录结构保存到本地"},
				&cli.BoolFlag{Name: "test", Usage: "仅测速不写本地文件"},
			},
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 {
					return fmt.Errorf("请指定至少一个待下载的网盘文件或目录")
				}
				saveTo := ""
				if c.Bool("save") {
					saveTo = "."
				} else if c.String("saveto") != "" {
					saveTo = filepath.Clean(c.String("saveto"))
				}
				return kkcommand.RunDownload(c.Args().Slice(), &kkcommand.DownloadOptions{
					SaveTo:      saveTo,
					Parallel:    c.Int("p"),
					Load:        c.Int("l"),
					MaxRetry:    c.Int("retry"),
					Overwrite:   c.Bool("ow"),
					NoCheck:     c.Bool("nocheck"),
					ModifyMTime: c.Bool("mtime"),
					FullPath:    c.Bool("fullpath"),
					TestOnly:    c.Bool("test"),
				})
			},
		},
		{
			Name:      "upload",
			Aliases:   []string{"u"},
			Usage:     "上传本地文件或目录 (支持秒传与阿里云 OSS 分片断点续传)",
			ArgsUsage: "<本地路径...> <目标网盘目录>",
			Category:  "上传与下载",
			Before:    reloadBefore,
			Flags: []cli.Flag{
				&cli.IntFlag{Name: "p", Usage: "单文件 OSS 分片上传并发数"},
				&cli.IntFlag{Name: "l", Usage: "同时上传的文件并发数"},
				&cli.IntFlag{Name: "retry", Value: 3, Usage: "分片失败最大重试次数"},
				&cli.BoolFlag{Name: "norapid", Usage: "禁用 MD5/SHA1 秒传校验直接上传"},
				&cli.StringFlag{Name: "policy", Usage: "重名处理策略: skip | overwrite | rename"},
			},
			Action: func(c *cli.Context) error {
				if c.NArg() < 2 {
					return fmt.Errorf("用法: upload <本地文件/目录...> <目标网盘目录>")
				}
				args := c.Args().Slice()
				return kkcommand.RunUpload(args[:len(args)-1], args[len(args)-1], &kkcommand.UploadOptions{
					Parallel:      c.Int("p"),
					Load:          c.Int("l"),
					MaxRetry:      c.Int("retry"),
					NoRapidUpload: c.Bool("norapid"),
					Policy:        c.String("policy"),
				})
			},
		},
	}
}
