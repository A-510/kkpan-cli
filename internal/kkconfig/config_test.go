package kkconfig

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kkpan-cli/kkpan/quarkpan"
)

func TestConfigSaveReloadAndAccountLifecycle(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "kkpan_config.json")
	mgr := NewManager(cfgPath)

	if err := mgr.Init(); err != nil {
		t.Fatalf("Init 失败: %v", err)
	}
	if err := mgr.SetAccount(Account{
		UID:        "u100",
		Nickname:   "星野",
		Cookie:     "__pus=secret_a; __puus=secret_b",
		MemberType: "SVIP",
	}); err != nil {
		t.Fatalf("SetAccount 失败: %v", err)
	}
	if err := mgr.SetAccount(Account{
		UID:        "u200",
		Nickname:   "绫波",
		Cookie:     "__pus=secret_c",
		MemberType: "NORMAL",
	}); err != nil {
		t.Fatalf("SetAccount 第二个账号失败: %v", err)
	}

	if err := mgr.UpdateWorkdir("/番剧/2026"); err != nil {
		t.Fatalf("UpdateWorkdir 失败: %v", err)
	}

	// 重新从磁盘加载验证持久化正确性
	reloaded := NewManager(cfgPath)
	if err := reloaded.Init(); err != nil {
		t.Fatalf("Reload 失败: %v", err)
	}
	active := reloaded.ActiveAccount()
	if active == nil || active.UID != "u200" || active.Workdir != "/番剧/2026" {
		t.Fatalf("活跃账号恢复异常: %+v", active)
	}

	// 验证 SwitchAccount 按昵称切换
	switched, err := reloaded.SwitchAccount("星野")
	if err != nil || switched.UID != "u100" {
		t.Fatalf("SwitchAccount 失败: %+v err=%v", switched, err)
	}

	// 验证 RemoveAccount
	removed, err := reloaded.RemoveAccount("u100")
	if err != nil || removed.UID != "u100" {
		t.Fatalf("RemoveAccount 失败: %+v err=%v", removed, err)
	}
	if cur := reloaded.ActiveAccount(); cur == nil || cur.UID != "u200" {
		t.Fatalf("注销后应回落至剩余账号 u200，实际: %+v", cur)
	}

	// 验证环境覆盖路径
	t.Setenv(EnvConfigDir, tmpDir)
	if got := GetConfigDir(); got != tmpDir {
		t.Fatalf("GetConfigDir 未遵循环境变量覆盖: %s", got)
	}
	_ = os.Remove(cfgPath)
}

func TestConfigCookieAutoPersistCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "__puus", Value: "auto_renewed_puus", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"member_type":"VIP","use_capacity":1,"total_capacity":100}}`))
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "kkpan_config.json")
	mgr := NewManager(cfgPath)
	_ = mgr.SetAccount(Account{
		UID:      "u1",
		Nickname: "Tester",
		Cookie:   "__pus=p1; __puus=old",
	})

	// 绑定回调测试客户端
	cli, err := quarkpan.NewClient(quarkpan.ClientOptions{
		DriveBaseURL: srv.URL,
		Cookie:       mgr.ActiveAccount().Cookie,
		OnCookieUpdated: func(newCookie string) {
			mgr.updateAccountCookie("u1", newCookie)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cli.GetMemberQuota(context.Background()); err != nil {
		t.Fatal(err)
	}

	reloaded := NewManager(cfgPath)
	_ = reloaded.Init()
	if acc := reloaded.ActiveAccount(); acc == nil || !strings.Contains(acc.Cookie, "auto_renewed_puus") {
		t.Fatalf("刷新后的 Cookie 未写回磁盘: %+v", acc)
	}
}

func TestParseByteRate(t *testing.T) {
	cases := map[string]int64{
		"0":        0,
		"20MB/s":   20 * 1024 * 1024,
		"512KB":    512 * 1024,
		"1.5GB/s":  int64(1.5 * 1024 * 1024 * 1024),
		"1048576":  1048576,
	}
	for input, want := range cases {
		got, err := ParseByteRate(input)
		if err != nil || got != want {
			t.Fatalf("ParseByteRate(%q) = %d, err=%v; want %d", input, got, err, want)
		}
	}
	if _, err := ParseByteRate("-10MB"); err == nil {
		t.Fatalf("负速率应报错")
	}
}
