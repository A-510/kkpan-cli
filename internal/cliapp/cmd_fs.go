package cliapp

import (
	"fmt"

	"github.com/kkpan-cli/kkpan/internal/kkcommand"
	"github.com/kkpan-cli/kkpan/internal/tui"
	"github.com/urfave/cli/v2"
)

func fsCommands(app *cli.App) []*cli.Command {
	return []*cli.Command{
		{
			Name:      "ui",
			Aliases:   []string{"tui", "browse"},
			Usage:     "启动日式二次元全屏交互式网盘档案浏览器 (含异步后台传输中心)",
			ArgsUsage: "[起始目录]",
			Category:  "浏览与管理",
			Before:    reloadBefore,
			Action: func(c *cli.Context) error {
				return tui.Run(c.Args().Get(0))
			},
		},
		{
			Name:      "ls",
			Aliases:   []string{"l", "ll"},
			Usage:     "列出网盘目录内容",
			ArgsUsage: "[目录路径...]",
			Category:  "浏览与管理",
			Before:    reloadBefore,
			Flags: append([]cli.Flag{
				&cli.BoolFlag{Name: "fid", Usage: "在表格中显示文件 FID"},
				&cli.BoolFlag{Name: "time", Usage: "按修改时间排序"},
				&cli.BoolFlag{Name: "size", Usage: "按文件体积排序"},
				&cli.BoolFlag{Name: "name", Usage: "按文件名排序"},
				&cli.BoolFlag{Name: "desc", Usage: "降序排列"},
				&cli.BoolFlag{Name: "refresh", Usage: "跳过缓存强制从服务端拉取"},
			}, outputFlags()...),
			Action: func(c *cli.Context) error {
				sortBy := ""
				switch {
				case c.Bool("time"):
					sortBy = "updated_at"
				case c.Bool("size"):
					sortBy = "size"
				case c.Bool("name"):
					sortBy = "file_name"
				}
				return kkcommand.RunLs(c.Args().Slice(), kkcommand.LsOptions{
					ShowFID: c.Bool("fid"),
					SortBy:  sortBy,
					Desc:    c.Bool("desc"),
					Refresh: c.Bool("refresh"),
				}, outputMode(c))
			},
		},
		{
			Name:      "cd",
			Usage:     "切换当前网盘工作目录",
			ArgsUsage: "<目录路径>",
			Category:  "浏览与管理",
			Before:    reloadBefore,
			Action: func(c *cli.Context) error {
				return kkcommand.RunCd(c.Args().Get(0))
			},
		},
		{
			Name:     "pwd",
			Usage:    "显示当前网盘工作目录",
			Category: "浏览与管理",
			Before:   reloadBefore,
			Action: func(c *cli.Context) error {
				return kkcommand.RunPwd()
			},
		},
		{
			Name:      "tree",
			Usage:     "树形展示网盘目录结构",
			ArgsUsage: "[目录路径]",
			Category:  "浏览与管理",
			Before:    reloadBefore,
			Flags: []cli.Flag{
				&cli.IntFlag{Name: "depth", Value: 2, Usage: "最大递归深度"},
				&cli.BoolFlag{Name: "fid", Usage: "附带显示 FID"},
			},
			Action: func(c *cli.Context) error {
				return kkcommand.RunTree(c.Args().Get(0), c.Int("depth"), c.Bool("fid"))
			},
		},
		{
			Name:      "search",
			Aliases:   []string{"s"},
			Usage:     "按关键字搜索全盘文件与目录",
			ArgsUsage: "<关键字>",
			Category:  "浏览与管理",
			Before:    reloadBefore,
			Flags: append([]cli.Flag{
				&cli.IntFlag{Name: "page", Value: 1, Usage: "页码"},
				&cli.IntFlag{Name: "size", Value: 50, Usage: "每页条数"},
			}, outputFlags()...),
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 {
					return fmt.Errorf("请输入搜索关键字")
				}
				return kkcommand.RunSearch(c.Args().Get(0), c.Int("page"), c.Int("size"), outputMode(c))
			},
		},
		{
			Name:      "meta",
			Usage:     "查看文件或目录完整元信息 (含 FID 与 MD5)",
			ArgsUsage: "<路径或 fid:<id>>",
			Category:  "浏览与管理",
			Before:    reloadBefore,
			Flags:     outputFlags(),
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 {
					return fmt.Errorf("请指定文件路径或 fid:<id>")
				}
				return kkcommand.RunMeta(c.Args().Get(0), outputMode(c))
			},
		},
		{
			Name:      "mkdir",
			Usage:     "创建网盘目录 (支持多级路径自动递归)",
			ArgsUsage: "<目录路径...>",
			Category:  "浏览与管理",
			Before:    reloadBefore,
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 {
					return fmt.Errorf("请指定需要创建的目录路径")
				}
				return kkcommand.RunMkdir(c.Args().Slice())
			},
		},
		{
			Name:      "rename",
			Aliases:   []string{"rn"},
			Usage:     "重命名文件或目录",
			ArgsUsage: "<原路径或 fid:<id>> <新名称>",
			Category:  "浏览与管理",
			Before:    reloadBefore,
			Action: func(c *cli.Context) error {
				if c.NArg() < 2 {
					return fmt.Errorf("用法: rename <原路径> <新名称>")
				}
				return kkcommand.RunRename(c.Args().Get(0), c.Args().Get(1))
			},
		},
		{
			Name:      "mv",
			Usage:     "移动文件或目录到指定目标目录",
			ArgsUsage: "<源路径...> <目标目录>",
			Category:  "浏览与管理",
			Before:    reloadBefore,
			Action: func(c *cli.Context) error {
				return kkcommand.RunMv(c.Args().Slice())
			},
		},
		{
			Name:      "cp",
			Usage:     "复制文件或目录到指定目标目录",
			ArgsUsage: "<源路径...> <目标目录>",
			Category:  "浏览与管理",
			Before:    reloadBefore,
			Action: func(c *cli.Context) error {
				return kkcommand.RunCp(c.Args().Slice())
			},
		},
		{
			Name:      "rm",
			Usage:     "删除文件或目录 (移入夸克网盘回收站)",
			ArgsUsage: "<路径...>",
			Category:  "浏览与管理",
			Before:    reloadBefore,
			Action: func(c *cli.Context) error {
				return kkcommand.RunRm(c.Args().Slice())
			},
		},
	}
}
