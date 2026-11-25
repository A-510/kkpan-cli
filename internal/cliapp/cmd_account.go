package cliapp

import (
	"fmt"

	"github.com/kkpan-cli/kkpan/internal/kkcommand"
	"github.com/urfave/cli/v2"
)

func accountCommands(app *cli.App) []*cli.Command {
	return []*cli.Command{
		{
			Name:     "login",
			Usage:    "登录夸克网盘账号 (支持终端扫码或 Cookie)",
			Category: "账号管理",
			Before:   reloadBefore,
			Flags: []cli.Flag{
				&cli.BoolFlag{
					Name:  "qr",
					Usage: "使用手机夸克 App 扫描终端二维码登录",
				},
				&cli.StringFlag{
					Name:  "cookies",
					Usage: "粘贴浏览器 pan.quark.cn 完整 Cookie 登录",
				},
			},
			Action: func(c *cli.Context) error {
				return kkcommand.RunLogin(kkcommand.LoginOptions{
					QR:      c.Bool("qr"),
					Cookies: c.String("cookies"),
				})
			},
		},
		{
			Name:     "who",
			Usage:    "查看当前活跃夸克账号信息",
			Category: "账号管理",
			Before:   reloadBefore,
			Flags:    outputFlags(),
			Action: func(c *cli.Context) error {
				return kkcommand.RunWho(outputMode(c))
			},
		},
		{
			Name:     "loglist",
			Usage:    "列出所有已登录的夸克账号",
			Category: "账号管理",
			Before:   reloadBefore,
			Flags:    outputFlags(),
			Action: func(c *cli.Context) error {
				return kkcommand.RunLogList(outputMode(c))
			},
		},
		{
			Name:      "su",
			Usage:     "切换当前活跃夸克账号",
			ArgsUsage: "<UID 或 昵称>",
			Category:  "账号管理",
			Before:    reloadBefore,
			Action: func(c *cli.Context) error {
				if c.NArg() < 1 {
					return fmt.Errorf("请指定目标账号 UID 或昵称")
				}
				return kkcommand.RunSu(c.Args().Get(0))
			},
		},
		{
			Name:      "logout",
			Usage:     "注销并删除本地保存的账号凭证",
			ArgsUsage: "[UID 或 昵称]",
			Category:  "账号管理",
			Before:    reloadBefore,
			Action: func(c *cli.Context) error {
				return kkcommand.RunLogout(c.Args().Get(0))
			},
		},
		{
			Name:     "quota",
			Usage:    "查询网盘容量配额与会员等级",
			Category: "账号管理",
			Before:   reloadBefore,
			Flags:    outputFlags(),
			Action: func(c *cli.Context) error {
				return kkcommand.RunQuota(outputMode(c))
			},
		},
	}
}
