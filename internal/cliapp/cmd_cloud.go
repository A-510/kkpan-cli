package cliapp

import (
	"fmt"

	"github.com/kkpan-cli/kkpan/internal/kkcommand"
	"github.com/urfave/cli/v2"
)

func cloudCommands(app *cli.App) []*cli.Command {
	return []*cli.Command{
		{
			Name:      "transfer",
			Aliases:   []string{"tf"},
			Usage:     "转存夸克网盘分享链接中的文件或目录",
			ArgsUsage: "<分享链接或整段分享文本> [提取码]",
			Category:  "分享与回收站",
			Before:    reloadBefore,
			Description: `支持免提取码链接、带 ?pwd= 参数的链接以及包含“提取码”的整段夸克分享口令文本。

示例:
  kkpan transfer https://pan.quark.cn/s/abc123def456
  kkpan transfer https://pan.quark.cn/s/abc123def456 9x8y
  kkpan transfer "我用夸克网盘分享了「资料」... 链接：https://pan.quark.cn/s/abc123def456 提取码：9x8y"
  kkpan transfer --list https://pan.quark.cn/s/abc123def456
  kkpan transfer --to /资源 --collect --download https://pan.quark.cn/s/abc123def456`,
			Flags: append([]cli.Flag{
				&cli.StringFlag{Name: "to", Usage: "指定转存目标网盘目录（默认为当前工作目录）"},
				&cli.BoolFlag{Name: "collect", Usage: "以分享标题创建独立子目录后再转存"},
				&cli.BoolFlag{Name: "download", Usage: "转存成功后立即下载到本地默认保存目录"},
				&cli.BoolFlag{Name: "list", Usage: "仅查看分享目录中的文件列表，不执行转存"},
				&cli.StringFlag{Name: "fid", Usage: "仅转存指定的分享条目 FID（多个可用逗号分隔）"},
				&cli.StringFlag{Name: "pdir", Usage: "指定查看或转存的分享子目录 FID（默认为根目录 0）"},
			}, outputFlags()...),
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 {
					return cli.ShowCommandHelp(c, c.Command.Name)
				}
				return kkcommand.RunShareTransfer(c.Args().Slice(), &kkcommand.TransferOptions{
					ToDir:     c.String("to"),
					Collect:   c.Bool("collect"),
					Download:  c.Bool("download"),
					ListOnly:  c.Bool("list"),
					FIDFilter: c.String("fid"),
					PDirFID:   c.String("pdir"),
				}, outputMode(c))
			},
		},
		{
			Name:     "share",
			Aliases:  []string{"sh"},
			Usage:    "创建、查看与取消网盘分享链接",
			Category: "分享与回收站",
			Before:   reloadBefore,
			Action: func(c *cli.Context) error {
				return cli.ShowCommandHelp(c, c.Command.Name)
			},
			Subcommands: []*cli.Command{
				{
					Name:      "set",
					Aliases:   []string{"s"},
					Usage:     "创建文件或目录的夸克分享链接",
					ArgsUsage: "<网盘路径或 fid:<id>...>",
					Description: `创建公开或加密的夸克网盘分享链接。
有效天数 (--period) 支持: 0 (永久, 默认), 1 (1天), 7 (7天), 30 (30天)。`,
					Flags: append([]cli.Flag{
						&cli.StringFlag{Name: "p", Aliases: []string{"passcode"}, Usage: "设置 4 位英文字母或数字提取码（留空则为公开链接）"},
						&cli.IntFlag{Name: "period", Value: 0, Usage: "有效天数（0=永久 / 1 / 7 / 30）"},
						&cli.StringFlag{Name: "title", Usage: "自定义分享标题（默认取首个文件或目录名）"},
						&cli.BoolFlag{Name: "f", Aliases: []string{"full"}, Usage: "在终端表格后附加完整可复制的夸克分享口令"},
					}, outputFlags()...),
					Action: func(c *cli.Context) error {
						if c.NArg() < 1 {
							return cli.ShowCommandHelp(c, c.Command.Name)
						}
						return kkcommand.RunShareSet(c.Args().Slice(), &kkcommand.ShareSetOptions{
							Passcode:   c.String("p"),
							PeriodDays: c.Int("period"),
							Title:      c.String("title"),
							FullFormat: c.Bool("f"),
						}, outputMode(c))
					},
				},
				{
					Name:    "list",
					Aliases: []string{"l", "ls"},
					Usage:   "列出当前账号创建的所有分享链接",
					Flags: append([]cli.Flag{
						&cli.IntFlag{Name: "page", Value: 1, Usage: "分享列表页码"},
						&cli.IntFlag{Name: "size", Value: 50, Usage: "每页条数"},
					}, outputFlags()...),
					Action: func(c *cli.Context) error {
						return kkcommand.RunShareList(c.Int("page"), c.Int("size"), outputMode(c))
					},
				},
				{
					Name:      "cancel",
					Aliases:   []string{"c", "del"},
					Usage:     "取消指定 Share ID 的分享链接",
					ArgsUsage: "<share_id...>",
					Action: func(c *cli.Context) error {
						if c.NArg() < 1 {
							return fmt.Errorf("请指定至少一个待取消的 Share ID")
						}
						return kkcommand.RunShareCancel(c.Args().Slice())
					},
				},
			},
		},
		{
			Name:     "recycle",
			Aliases:  []string{"rc"},
			Usage:    "查看、还原与清理网盘回收站",
			Category: "分享与回收站",
			Before:   reloadBefore,
			Action: func(c *cli.Context) error {
				return cli.ShowCommandHelp(c, c.Command.Name)
			},
			Subcommands: []*cli.Command{
				{
					Name:    "list",
					Aliases: []string{"l", "ls"},
					Usage:   "列出回收站中的文件与目录",
					Flags: append([]cli.Flag{
						&cli.IntFlag{Name: "page", Value: 1, Usage: "回收站列表页码"},
						&cli.IntFlag{Name: "size", Value: 50, Usage: "每页条数"},
					}, outputFlags()...),
					Action: func(c *cli.Context) error {
						return kkcommand.RunRecycleList(c.Int("page"), c.Int("size"), outputMode(c))
					},
				},
				{
					Name:      "restore",
					Aliases:   []string{"r"},
					Usage:     "从回收站还原指定 Record ID 的文件或目录",
					ArgsUsage: "<record_id...>",
					Action: func(c *cli.Context) error {
						if c.NArg() < 1 {
							return fmt.Errorf("请指定至少一个待还原的 Record ID")
						}
						return kkcommand.RunRecycleRestore(c.Args().Slice())
					},
				},
				{
					Name:        "delete",
					Aliases:     []string{"d", "del"},
					Usage:       "彻底删除回收站记录或清空回收站（不可恢复）",
					ArgsUsage:   "[--all] [record_id...]",
					Description: "警告：彻底删除与 --all 清空操作不可恢复，且执行时不进行二次确认。",
					Flags: []cli.Flag{
						&cli.BoolFlag{Name: "all", Usage: "清空回收站全部记录（高危，不可恢复）"},
					},
					Action: func(c *cli.Context) error {
						if c.Bool("all") {
							return kkcommand.RunRecycleDelete(nil, true)
						}
						if c.NArg() < 1 {
							return cli.ShowCommandHelp(c, c.Command.Name)
						}
						return kkcommand.RunRecycleDelete(c.Args().Slice(), false)
					},
				},
			},
		},
	}
}
