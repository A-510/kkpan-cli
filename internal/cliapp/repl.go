package cliapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/kkpan-cli/kkpan/internal/kkconfig"
	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
	"github.com/peterh/liner"
	"github.com/urfave/cli/v2"
)

func runREPL(c *cli.Context) error {
	app := c.App
	if c.NArg() != 0 {
		return fmt.Errorf("未找到命令: %s", c.Args().Get(0))
	}

	line := liner.NewLiner()
	line.SetCtrlCAborts(true)
	line.SetTabCompletionStyle(liner.TabCircular)

	historyPath := filepath.Join(kkconfig.GetConfigDir(), kkconfig.HistoryFileName)
	if f, err := os.Open(historyPath); err == nil {
		_, _ = line.ReadHistory(f)
		_ = f.Close()
	}
	defer func() {
		_ = os.MkdirAll(filepath.Dir(historyPath), 0700)
		if f, err := os.OpenFile(historyPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600); err == nil {
			_, _ = line.WriteHistory(f)
			_ = f.Close()
		}
		_ = line.Close()
	}()

	cmdNames := collectCommandNames(app)
	line.SetCompleter(func(input string) []string {
		trimmed := strings.TrimLeftFunc(input, unicode.IsSpace)
		if strings.ContainsRune(trimmed, ' ') {
			return nil
		}
		var out []string
		for _, name := range cmdNames {
			if strings.HasPrefix(name, trimmed) {
				out = append(out, name)
			}
		}
		return out
	})

	var quotaCache *quarkpan.MemberQuota
	var quotaUID string
	refreshQuota := func() {
		acc := kkconfig.Manager.ActiveAccount()
		if acc == nil || acc.Nickname == "" {
			quotaCache = nil
			quotaUID = ""
			return
		}
		if quotaUID == acc.UID && quotaCache != nil {
			return
		}
		quotaUID = acc.UID
		if cliObj, err := kkconfig.Manager.ActiveClient(); err == nil && cliObj.HasCookie() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if q, err := cliObj.GetMemberQuota(ctx); err == nil {
				quotaCache = q
			}
		}
	}
	refreshQuota()

	if !ui.IsQuiet() {
		acc := kkconfig.Manager.ActiveAccount()
		bInfo := ui.BannerInfo{
			AppName:  app.Name,
			Version:  app.Version,
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Theme:    ui.EffectiveTheme(kkconfig.Manager.Theme).Name,
		}
		if acc != nil && acc.Nickname != "" {
			bInfo.LoggedIn = true
			bInfo.Nickname = acc.Nickname
			bInfo.UID = acc.UID
			bInfo.MemberType = acc.MemberType
			bInfo.Workdir = acc.Workdir
		}
		if quotaCache != nil && quotaCache.TotalCapacity > 0 {
			bInfo.HasQuota = true
			bInfo.QuotaUsed = quotaCache.UseCapacity
			bInfo.QuotaTotal = quotaCache.TotalCapacity
		}
		ui.PrintREPLBanner(os.Stdout, os.Stdin, bInfo)
	}

	baseSnap := ui.SnapshotOverrides()

	// 阻止在 REPL 内无子命令输入时重入嵌套 REPL
	origAction := app.Action
	app.Action = func(sub *cli.Context) error {
		if sub.NArg() != 0 {
			return fmt.Errorf("未找到命令: %s", sub.Args().Get(0))
		}
		return fmt.Errorf("请输入具体命令（输入 'help' 查看全部命令，'ui' 进入全屏界面）")
	}
	defer func() { app.Action = origAction }()

	for {
		refreshQuota()
		acc := kkconfig.Manager.ActiveAccount()
		pCtx := ui.PromptContext{
			Theme: ui.EffectiveTheme(kkconfig.Manager.Theme).Name,
		}
		if acc != nil && acc.Nickname != "" {
			pCtx.LoggedIn = true
			pCtx.Nickname = acc.Nickname
			pCtx.MemberType = acc.MemberType
			pCtx.Workdir = acc.Workdir
		}
		if quotaCache != nil && quotaCache.TotalCapacity > 0 {
			pCtx.HasQuota = true
			pCtx.QuotaUsed = quotaCache.UseCapacity
			pCtx.QuotaTotal = quotaCache.TotalCapacity
		}

		hdr, prompt := ui.FormatPrompt(os.Stdout, pCtx)
		if hdr != "" {
			fmt.Fprintln(os.Stdout, hdr)
		}

		rawLine, err := line.Prompt(prompt)
		if errors.Is(err, liner.ErrPromptAborted) || errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		rawLine = strings.TrimSpace(rawLine)
		if rawLine == "" {
			continue
		}
		line.AppendHistory(rawLine)

		tokens := splitShellArgs(rawLine)
		if len(tokens) == 0 {
			continue
		}
		switch tokens[0] {
		case "exit", "quit":
			return nil
		case "clear", "cls":
			fmt.Print("\x1b[2J\x1b[H")
			continue
		}

		runArgs := append([]string{app.Name}, tokens...)
		if err := app.Run(runArgs); err != nil {
			ui.PrintError(os.Stderr, "命令执行失败", err, "")
		}
		ui.RestoreOverrides(baseSnap)
		applyConfigUITheme()
	}
}

func collectCommandNames(app *cli.App) []string {
	seen := make(map[string]struct{})
	var out []string
	add := func(s string) {
		if _, ok := seen[s]; !ok && s != "" {
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	for _, cmd := range app.Commands {
		add(cmd.Name)
		for _, a := range cmd.Aliases {
			add(a)
		}
	}
	for _, builtin := range []string{"help", "exit", "quit", "clear"} {
		add(builtin)
	}
	return out
}

// splitShellArgs 解析带单双引号及反斜杠转义的交互输入行
func splitShellArgs(line string) []string {
	var args []string
	var cur strings.Builder
	var quote rune
	escaped := false
	hasToken := false

	for _, r := range line {
		if escaped {
			if r == '\\' || r == '"' || r == '\'' || unicode.IsSpace(r) {
				cur.WriteRune(r)
			} else {
				// 保留 Windows 路径分隔符（如 C:\my\demo.mp4）中的反斜杠，不吞掉路径字符
				cur.WriteRune('\\')
				cur.WriteRune(r)
			}
			escaped = false
			hasToken = true
			continue
		}
		if r == '\\' && quote != '\'' {
			escaped = true
			hasToken = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
			hasToken = true
			continue
		}
		if r == '\'' || r == '"' {
			quote = r
			hasToken = true
			continue
		}
		if unicode.IsSpace(r) {
			if hasToken {
				args = append(args, cur.String())
				cur.Reset()
				hasToken = false
			}
			continue
		}
		cur.WriteRune(r)
		hasToken = true
	}
	if hasToken {
		args = append(args, cur.String())
	}
	return args
}
