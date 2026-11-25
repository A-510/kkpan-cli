package cliapp

import (
	"fmt"
	"strings"

	"github.com/kkpan-cli/kkpan/internal/kkcommand"
	"github.com/urfave/cli/v2"
)

func systemCommands(app *cli.App) []*cli.Command {
	return []*cli.Command{
		{
			Name:     "config",
			Usage:    "查看、修改或重置客户端运行配置",
			Category: "系统与工具",
			Before:   reloadBefore,
			Action: func(c *cli.Context) error {
				return kkcommand.RunConfigShow()
			},
			Subcommands: []*cli.Command{
				{
					Name:  "set",
					Usage: "修改配置项 (示例: kkpan config set -theme shinbo -max_parallel 2)",
					Flags: []cli.Flag{
						&cli.StringFlag{Name: "savedir", Usage: "默认下载目录"},
						&cli.IntFlag{Name: "max_parallel", Usage: "下载分片线程数"},
						&cli.IntFlag{Name: "max_upload_parallel", Usage: "上传分片并发数"},
						&cli.IntFlag{Name: "max_download_load", Usage: "同时下载文件数量"},
						&cli.IntFlag{Name: "max_upload_load", Usage: "同时上传文件数量"},
						&cli.StringFlag{Name: "max_download_rate", Usage: "下载限速 (如 20MB/s 或 0)"},
						&cli.StringFlag{Name: "max_upload_rate", Usage: "上传限速 (如 10MB/s 或 0)"},
						&cli.StringFlag{Name: "u_policy", Usage: "上传重名策略 (skip/overwrite/rename)"},
						&cli.StringFlag{Name: "no_check", Usage: "是否禁用下载 MD5 校验 (true/false)"},
						&cli.StringFlag{Name: "modify_mtime", Usage: "是否同步服务器修改时间 (true/false)"},
						&cli.StringFlag{Name: "proxy", Usage: "HTTP/SOCKS5 代理地址"},
						&cli.StringFlag{Name: "theme", Usage: "日式二次元主题 (celestial/dossier/shinbo)"},
						&cli.StringFlag{Name: "prompt_style", Usage: "REPL 提示符风格 (starship/boxed)"},
					},
					Action: func(c *cli.Context) error {
						updates := make(map[string]string)
						for _, f := range c.Command.Flags {
							for _, name := range f.Names() {
								if c.IsSet(name) {
									updates[name] = c.String(name)
									break
								}
							}
						}
						// 同时支持位置参数 key=value 写法
						for _, arg := range c.Args().Slice() {
							if eq := strings.IndexByte(arg, '='); eq > 0 {
								updates[strings.TrimPrefix(arg[:eq], "-")] = arg[eq+1:]
							}
						}
						return kkcommand.RunConfigSet(updates)
					},
				},
				{
					Name:  "reset",
					Usage: "重置常规配置为默认值 (保留已登录账号)",
					Action: func(c *cli.Context) error {
						return kkcommand.RunConfigReset()
					},
				},
			},
		},
		{
			Name:     "env",
			Usage:    "查看环境变量配置状态",
			Category: "系统与工具",
			Before:   reloadBefore,
			Action: func(c *cli.Context) error {
				return kkcommand.RunEnv()
			},
		},
		{
			Name:      "sumfile",
			Usage:     "计算本地文件大小、MD5 与 SHA1 (用于核对夸克秒传哈希)",
			ArgsUsage: "<本地文件...>",
			Category:  "系统与工具",
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 {
					return fmt.Errorf("请指定至少一个本地文件路径")
				}
				return kkcommand.RunSumFile(c.Args().Slice())
			},
		},
	}
}
