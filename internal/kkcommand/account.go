package kkcommand

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/kkpan-cli/kkpan/internal/kkconfig"
	"github.com/kkpan-cli/kkpan/internal/ui"
	"github.com/kkpan-cli/kkpan/quarkpan"
)

// LoginOptions 控制登录命令的行为模式
type LoginOptions struct {
	QR      bool
	Cookies string
}

// RunLogin 执行夸克网盘扫码登录、Cookie 登录或交互式登录选择
func RunLogin(opt LoginOptions) error {
	if strings.TrimSpace(opt.Cookies) != "" {
		return loginWithCookie(opt.Cookies)
	}
	if opt.QR {
		return loginWithQR()
	}
	if !ui.IsTTY(os.Stdin) || !ui.IsTTY(os.Stdout) {
		return fmt.Errorf("非交互终端下请使用 'kkpan login -qr' 或 'kkpan login -cookies \"...\"'")
	}

	fmt.Fprintln(os.Stderr, "请选择夸克网盘登录方式:")
	fmt.Fprintln(os.Stderr, "  1. 终端二维码扫码登录 (推荐)")
	fmt.Fprintln(os.Stderr, "  2. 粘贴浏览器 Cookies 登录")
	fmt.Fprint(os.Stderr, "请输入选项 [1/2] (默认 1): ")

	reader := bufio.NewReader(os.Stdin)
	choice, _ := reader.ReadString('\n')
	switch strings.TrimSpace(choice) {
	case "", "1":
		return loginWithQR()
	case "2":
		fmt.Fprint(os.Stderr, "请粘贴 pan.quark.cn 完整 Cookie (回车确认): ")
		rawCookie, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		return loginWithCookie(rawCookie)
	default:
		return fmt.Errorf("无效选项: %s", strings.TrimSpace(choice))
	}
}

func loginWithCookie(rawCookie string) error {
	trimmed := strings.TrimSpace(rawCookie)
	if trimmed == "" {
		return fmt.Errorf("Cookie 不能为空")
	}

	cli, err := kkconfig.Manager.NewAnonClient()
	if err != nil {
		return err
	}
	cli.SetCookie(trimmed)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	info, err := cli.GetAccountInfo(ctx)
	if err != nil {
		return fmt.Errorf("验证 Cookie 失败: %w", err)
	}
	memberType := "NORMAL"
	if q, qErr := cli.GetMemberQuota(ctx); qErr == nil && q != nil {
		memberType = q.MemberType
	}

	if err := kkconfig.Manager.SetAccount(kkconfig.Account{
		UID:        info.UID,
		Nickname:   info.Nickname,
		Cookie:     cli.CookieHeader(),
		Workdir:    "/",
		MemberType: memberType,
	}); err != nil {
		return err
	}
	ui.PrintSuccess(os.Stderr, "登录成功！账号: %s (UID: %s, 等级: %s)", info.Nickname, ui.MaskUID(info.UID), memberType)
	return nil
}

func loginWithQR() error {
	cli, err := kkconfig.Manager.NewAnonClient()
	if err != nil {
		return err
	}

	sigCtx, stopSig := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stopSig()
	ctx, cancel := context.WithTimeout(sigCtx, 3*time.Minute)
	defer cancel()

	sess, err := cli.GetQRToken(ctx)
	if err != nil {
		return err
	}

	qrPath := filepath.Join(kkconfig.GetConfigDir(), "qrcode.png")
	_ = os.MkdirAll(filepath.Dir(qrPath), 0700)
	if err := ui.WriteQRPNGFile(sess.QRURL, qrPath); err != nil {
		return err
	}
	defer func() {
		_ = os.Remove(qrPath)
	}()

	asciiOn := ui.ASCIIOnly()
	colorOn := ui.ColorEnabled(os.Stderr)
	if asciiOn {
		ui.PrintInfo(os.Stderr, "当前为纯 ASCII 模式，请使用夸克 App 扫描本地二维码图片: %s", qrPath)
	} else {
		ui.PrintInfo(os.Stderr, "请使用「手机夸克 App」扫描下方二维码确认登录:")
		rendered, qrW, matErr := ui.RenderQRMatrixTerminal(os.Stderr, sess.QRURL, colorOn, false)
		if matErr == nil && rendered != "" {
			fmt.Fprintln(os.Stderr, rendered)
		} else if qrW > 0 {
			ui.PrintWarn(os.Stderr, "终端宽度不足以完整显示二维码 (需 %d 列)，请拉宽终端或打开图片扫码: %s", qrW, qrPath)
		}
		ui.PrintSuccess(os.Stderr, "临时二维码图片已生成至 %s (登录结束或取消后自动清理)", qrPath)
	}
	ui.PrintInfo(os.Stderr, "等待扫码确认中 (按 Ctrl+C 取消)...")

	res, err := cli.RunQRLoginLoop(ctx, sess, 2*time.Second)
	if err != nil {
		return err
	}

	memberType := "NORMAL"
	if q, qErr := cli.GetMemberQuota(ctx); qErr == nil && q != nil {
		memberType = q.MemberType
	}
	if err := kkconfig.Manager.SetAccount(kkconfig.Account{
		UID:        res.Account.UID,
		Nickname:   res.Account.Nickname,
		Cookie:     res.Cookie,
		Workdir:    "/",
		MemberType: memberType,
	}); err != nil {
		return err
	}
	ui.PrintSuccess(os.Stderr, "扫码登录成功！欢迎回来，%s (%s)", res.Account.Nickname, memberType)
	return nil
}

// WhoInfo 描述 who 命令的结构化输出（不含任何 Cookie 敏感信息）
type WhoInfo struct {
	UID        string `json:"uid"`
	Nickname   string `json:"nickname"`
	MemberType string `json:"member_type"`
	Workdir    string `json:"workdir"`
	LoggedIn   bool   `json:"logged_in"`
}

// RunWho 输出当前活跃账号信息
func RunWho(mode ui.OutputMode) error {
	acc := kkconfig.Manager.ActiveAccount()
	info := WhoInfo{Workdir: "/"}
	if acc != nil && acc.Nickname != "" {
		info.UID = acc.UID
		info.Nickname = acc.Nickname
		info.MemberType = acc.MemberType
		info.Workdir = acc.Workdir
		info.LoggedIn = true
	}

	switch mode {
	case ui.OutputJSON:
		return ui.WriteJSON(os.Stdout, info)
	case ui.OutputPlain:
		return ui.WriteTSV(os.Stdout, [][]string{{info.UID, info.Nickname, info.MemberType, info.Workdir}})
	default:
		if !info.LoggedIn {
			ui.PrintWarn(os.Stderr, "当前尚未登录夸克账号，请执行 'kkpan login -qr'")
			return nil
		}
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Headers: []string{"UID", "昵称", "会员等级", "工作目录"},
			Rows: [][]string{
				{ui.MaskUID(info.UID), info.Nickname, info.MemberType, info.Workdir},
			},
		}))
		return nil
	}
}

// AccountSummary 描述已登录账号摘要（脱敏，供 loglist --json 使用）
type AccountSummary struct {
	Active     bool   `json:"active"`
	UID        string `json:"uid"`
	Nickname   string `json:"nickname"`
	MemberType string `json:"member_type"`
	Workdir    string `json:"workdir"`
}

// RunLogList 列出所有本地已保存账号
func RunLogList(mode ui.OutputMode) error {
	active := kkconfig.Manager.ActiveAccount()
	activeUID := ""
	if active != nil {
		activeUID = active.UID
	}

	items := make([]AccountSummary, 0, len(kkconfig.Manager.Accounts))
	rows := make([][]string, 0, len(kkconfig.Manager.Accounts))
	for _, a := range kkconfig.Manager.Accounts {
		isAct := a.UID == activeUID
		items = append(items, AccountSummary{
			Active:     isAct,
			UID:        a.UID,
			Nickname:   a.Nickname,
			MemberType: a.MemberType,
			Workdir:    a.Workdir,
		})
		mark := ""
		if isAct {
			mark = "*"
		}
		rows = append(rows, []string{mark, ui.MaskUID(a.UID), a.Nickname, a.MemberType, a.Workdir})
	}

	switch mode {
	case ui.OutputJSON:
		return ui.WriteJSON(os.Stdout, items)
	case ui.OutputPlain:
		tsv := make([][]string, 0, len(items))
		for _, it := range items {
			act := "0"
			if it.Active {
				act = "1"
			}
			tsv = append(tsv, []string{act, it.UID, it.Nickname, it.MemberType, it.Workdir})
		}
		return ui.WriteTSV(os.Stdout, tsv)
	default:
		if len(rows) == 0 {
			ui.PrintInfo(os.Stdout, "暂无已登录账号")
			return nil
		}
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Headers: []string{"当前", "UID", "昵称", "会员等级", "工作目录"},
			Rows:    rows,
		}))
		return nil
	}
}

// RunSu 切换活跃账号
func RunSu(target string) error {
	acc, err := kkconfig.Manager.SwitchAccount(target)
	if err != nil {
		return err
	}
	ui.PrintSuccess(os.Stdout, "已切换账号为 %s (UID: %s, 工作目录: %s)", acc.Nickname, ui.MaskUID(acc.UID), acc.Workdir)
	return nil
}

// RunLogout 注销指定或当前活跃账号
func RunLogout(target string) error {
	removed, err := kkconfig.Manager.RemoveAccount(target)
	if err != nil {
		return err
	}
	ui.PrintSuccess(os.Stdout, "已注销账号 %s (%s)", removed.Nickname, ui.MaskUID(removed.UID))
	return nil
}

// QuotaView 描述容量查询输出结构
type QuotaView struct {
	UseCapacity   int64   `json:"use_capacity"`
	TotalCapacity int64   `json:"total_capacity"`
	UsedHuman     string  `json:"used_human"`
	TotalHuman    string  `json:"total_human"`
	Percent       float64 `json:"percent"`
	MemberType    string  `json:"member_type"`
	IsVIP         bool    `json:"is_vip"`
	IsSVIP        bool    `json:"is_svip"`
}

// RunQuota 查询并展示夸克网盘空间配额
func RunQuota(mode ui.OutputMode) error {
	cli, err := requireActiveClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	q, err := cli.GetMemberQuota(ctx)
	if err != nil {
		return err
	}
	var pct float64
	if q.TotalCapacity > 0 {
		pct = float64(q.UseCapacity) / float64(q.TotalCapacity) * 100
	}
	view := QuotaView{
		UseCapacity:   q.UseCapacity,
		TotalCapacity: q.TotalCapacity,
		UsedHuman:     ui.FormatBytes(q.UseCapacity),
		TotalHuman:    ui.FormatBytes(q.TotalCapacity),
		Percent:       pct,
		MemberType:    q.MemberType,
		IsVIP:         q.IsVIP,
		IsSVIP:        q.IsSVIP,
	}

	switch mode {
	case ui.OutputJSON:
		return ui.WriteJSON(os.Stdout, view)
	case ui.OutputPlain:
		return ui.WriteTSV(os.Stdout, [][]string{{
			fmt.Sprintf("%d", view.UseCapacity),
			fmt.Sprintf("%d", view.TotalCapacity),
			fmt.Sprintf("%.2f%%", view.Percent),
			view.MemberType,
		}})
	default:
		bar := ui.RenderQuotaBar(os.Stdout, pct/100, 20, ui.ColorEnabled(os.Stdout), ui.ASCIIOnly())
		fmt.Print(ui.RenderTable(os.Stdout, ui.TableConfig{
			Headers: []string{"会员等级", "已用空间", "总容量", "使用率", "星轨进度"},
			Rows: [][]string{
				{view.MemberType, view.UsedHuman, view.TotalHuman, fmt.Sprintf("%.2f%%", view.Percent), bar},
			},
		}))
		return nil
	}
}

func requireActiveClient() (*quarkpan.Client, error) {
	cli, err := kkconfig.Manager.ActiveClient()
	if err != nil {
		return nil, err
	}
	if !cli.HasCookie() {
		return nil, quarkpan.ErrNotLoggedIn
	}
	return cli, nil
}
