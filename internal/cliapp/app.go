package cliapp

import (
	"errors"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/kkpan-cli/kkpan/internal/kkconfig"
	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/urfave/cli/v2"
)

// InitConfig 初始化本地配置文件
func InitConfig() {
	if err := kkconfig.Manager.Init(); err != nil {
		ui.PrintError(os.Stderr, "配置文件读取失败", err, "检查配置文件权限或修复 JSON 语法错误")
		os.Exit(1)
	}
}

func applyConfigUITheme() {
	// 若命令行未覆盖主题且环境变量未指定，同步配置文件中的主题到全局覆盖层以便非 TUI 命令使用
	if ui.GetThemeOverride() == "" && os.Getenv(ui.EnvTheme) == "" && kkconfig.Manager.Theme != "" {
		ui.SetThemeOverride(kkconfig.Manager.Theme)
	}
}

// NewApp 创建并装配 kkpan 命令行应用
func NewApp(version string) *cli.App {
	app := &cli.App{
		Name:      ui.AppName,
		HelpName:  ui.AppName,
		Version:   version,
		Usage:     "夸克网盘 Go 命令行与日式二次元全屏终端客户端 (" + runtime.GOOS + "/" + runtime.GOARCH + ")",
		Copyright: "Apache-2.0 License",
		Description: `kkpan-cli (kkpan) 是基于 Go 构建的现代夸克网盘命令行与交互式终端客户端。
  支持终端扫码/Cookie 登录、路径与 FID 双向解析、多线程断点续传下载、MD5/SHA1 秒传与 OSS 分片上传、
  --json/--plain 脚本自动化输出，以及基于 Bubble Tea 的日式二次元双栏全屏档案浏览器 (kkpan ui)。`,
		ErrWriter: os.Stderr,
		Flags: []cli.Flag{
			&cli.BoolFlag{
				Name:    "quiet",
				Aliases: []string{"q"},
				Usage:   "静默模式，隐藏启动横幅与实时进度条",
			},
			&cli.BoolFlag{
				Name:  "no-color",
				Usage: "禁用终端 ANSI 彩色输出 (兼容 NO_COLOR)",
			},
			&cli.BoolFlag{
				Name:  "ascii",
				Usage: "强制纯 7-bit ASCII 符号边框与降级显示 (兼容 KKPAN_ASCII=1)",
			},
			&cli.StringFlag{
				Name:  "theme",
				Usage: "指定日式二次元视觉主题 (celestial / dossier / shinbo)",
			},
			&cli.StringFlag{
				Name:    "prompt-style",
				Aliases: []string{"prompt_style"},
				Usage:   "指定 REPL 提示符风格 (starship / boxed)",
			},
		},
		Before: func(c *cli.Context) error {
			if c.Bool("no-color") {
				ui.SetNoColor(true)
			}
			if c.Bool("ascii") {
				ui.SetASCIIOnly(true)
			}
			if c.Bool("quiet") {
				ui.SetQuiet(true)
			}
			if c.IsSet("theme") {
				if _, err := ui.ParseThemeName(c.String("theme")); err != nil {
					return err
				}
				ui.SetThemeOverride(c.String("theme"))
			}
			if c.IsSet("prompt-style") {
				if _, err := ui.ParsePromptStyle(c.String("prompt-style")); err != nil {
					return err
				}
				ui.SetPromptStyleOverride(c.String("prompt-style"))
			}
			applyConfigUITheme()
			return nil
		},
		Action: runREPL,
	}

	var cmds []*cli.Command
	cmds = append(cmds, accountCommands(app)...)
	cmds = append(cmds, fsCommands(app)...)
	cmds = append(cmds, transferCommands(app)...)
	cmds = append(cmds, btCommands(app)...)
	cmds = append(cmds, cloudCommands(app)...)
	cmds = append(cmds, systemCommands(app)...)
	sort.Sort(cli.FlagsByName(app.Flags))
	sort.Sort(cli.CommandsByName(cmds))
	app.Commands = cmds
	return app
}

func outputFlags() []cli.Flag {
	return []cli.Flag{
		&cli.BoolFlag{
			Name:  "json",
			Usage: "以结构化 JSON 输出结果 (适配脚本与 AI Agent 管道)",
		},
		&cli.BoolFlag{
			Name:  "plain",
			Usage: "以无边框制表符分隔 (TSV) 格式输出",
		},
	}
}

func outputMode(c *cli.Context) ui.OutputMode {
	return ui.ResolveOutputMode(c.Bool("json"), c.Bool("plain"))
}

func reloadBefore(c *cli.Context) error {
	if err := kkconfig.Manager.Reload(); err != nil {
		ui.PrintWarn(os.Stderr, "重载配置警告: %s", err)
	}
	applyConfigUITheme()
	return nil
}

// preScanGlobalFlags 在初始化配置前预扫描顶层全局标志，遇到 "--" 或子命令词元立即停止
func preScanGlobalFlags(args []string) {
	for i := 1; i < len(args); i++ {
		tok := args[i]
		if tok == "--" || !strings.HasPrefix(tok, "-") || tok == "-" {
			break
		}
		trimmed := strings.TrimLeft(tok, "-")
		name := trimmed
		eqVal := ""
		hasEq := false
		if eq := strings.IndexByte(trimmed, '='); eq >= 0 {
			name = trimmed[:eq]
			eqVal = trimmed[eq+1:]
			hasEq = true
		}

		if name == "theme" || name == "prompt-style" || name == "prompt_style" {
			val := eqVal
			if !hasEq {
				if i+1 >= len(args) {
					break
				}
				i++
				val = args[i]
			}
			if name == "theme" {
				ui.SetThemeOverride(val)
			} else {
				ui.SetPromptStyleOverride(val)
			}
			continue
		}

		val := true
		if hasEq {
			parsed, err := strconv.ParseBool(eqVal)
			if err != nil {
				continue
			}
			val = parsed
		}
		switch name {
		case "no-color":
			ui.SetNoColor(val)
		case "ascii":
			ui.SetASCIIOnly(val)
		case "quiet", "q":
			ui.SetQuiet(val)
		}
	}
}

// Run 初始化配置并执行 CLI 应用，所有未处理的错误统一结构化输出到 stderr
func Run(version string, args []string) error {
	preScanGlobalFlags(args)
	InitConfig()

	app := NewApp(version)
	err := app.Run(args)
	if err != nil {
		var exitCoder cli.ExitCoder
		if !errors.As(err, &exitCoder) || exitCoder.ExitCode() != 0 {
			ui.PrintError(os.Stderr, "命令执行失败", err, "")
		}
	}
	return err
}
