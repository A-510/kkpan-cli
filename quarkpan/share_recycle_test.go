package quarkpan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestParseShareInput(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantID   string
		wantPass string
		wantErr  bool
	}{
		{
			name:   "裸分享链接",
			args:   []string{"https://pan.quark.cn/s/abc123def456"},
			wantID: "abc123def456",
		},
		{
			name:   "带哈希路由分享链接",
			args:   []string{"https://pan.quark.cn/s/abc123def456#/list/share"},
			wantID: "abc123def456",
		},
		{
			name:     "URL查询参数提取码",
			args:     []string{"https://pan.quark.cn/s/abc123def456?pwd=9x8y"},
			wantID:   "abc123def456",
			wantPass: "9x8y",
		},
		{
			name:     "整段中文夸克分享口令",
			args:     []string{"我用夸克网盘给你分享了「项目资料」，点击链接即可保存。\n链接：https://pan.quark.cn/s/xyz987uvw654\n提取码：Ab12"},
			wantID:   "xyz987uvw654",
			wantPass: "Ab12",
		},
		{
			name:     "独立参数提取码覆盖URL参数",
			args:     []string{"https://pan.quark.cn/s/abc123def456?pwd=0000", "7k8m"},
			wantID:   "abc123def456",
			wantPass: "7k8m",
		},
		{
			name:     "直接传入裸pwd_id与提取码",
			args:     []string{"abc123def456", "Q1w2"},
			wantID:   "abc123def456",
			wantPass: "Q1w2",
		},
		{
			name:    "空参数报错",
			args:    nil,
			wantErr: true,
		},
		{
			name:    "无法识别的文本报错",
			args:    []string{"hello world"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotID, gotPass, err := ParseShareInput(tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("期望报错但返回成功: id=%s pass=%s", gotID, gotPass)
				}
				return
			}
			if err != nil {
				t.Fatalf("意外错误: %v", err)
			}
			if gotID != tc.wantID || gotPass != tc.wantPass {
				t.Fatalf("解析结果不符: got=(%q, %q), want=(%q, %q)", gotID, gotPass, tc.wantID, tc.wantPass)
			}
		})
	}
}

func TestShareTransferFlow(t *testing.T) {
	var listCalls int
	var sortCalls int
	taskPollCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/1/clouddrive/share/sharepage/token":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["pwd_id"] != "pwd123456" || body["passcode"] != "1a2b" {
				t.Fatalf("token 请求体异常: %+v", body)
			}
			writeEnvelope(w, 200, 0, "", map[string]interface{}{
				"stoken": "stk-001",
				"title":  "测试资源合集",
			})

		case r.Method == http.MethodGet && r.URL.Path == "/1/clouddrive/share/sharepage/detail":
			if r.URL.Query().Get("pwd_id") != "pwd123456" || r.URL.Query().Get("stoken") != "stk-001" {
				t.Fatalf("detail 查询参数异常: %s", r.URL.RawQuery)
			}
			page := r.URL.Query().Get("_page")
			if page == "1" {
				// 构造 100 条以触发自动翻页，metadata._total=101
				items := make([]map[string]interface{}, 0, defaultListPageSize)
				for i := 0; i < defaultListPageSize; i++ {
					items = append(items, map[string]interface{}{
						"fid":             fmt.Sprintf("sfid-%03d", i+1),
						"pdir_fid":        "0",
						"file_name":       fmt.Sprintf("part-%03d.bin", i+1),
						"size":            1024,
						"dir":             false,
						"share_fid_token": fmt.Sprintf("ftok-%03d", i+1),
					})
				}
				_, _ = fmt.Fprintf(w, `{"status":200,"code":0,"data":{"share":{"pwd_id":"pwd123456","title":"测试资源合集"},"list":%s},"metadata":{"_total":101,"_count":100,"_page":1,"_size":100}}`, mustJSON(t, items))
				return
			}
			_, _ = fmt.Fprintf(w, `{"status":200,"code":0,"data":{"share":{"pwd_id":"pwd123456","title":"测试资源合集"},"list":[{"fid":"sfid-101","pdir_fid":"0","file_name":"readme.txt","size":512,"dir":false,"share_fid_token":"ftok-101"}]},"metadata":{"_total":101,"_count":1,"_page":2,"_size":100}}`)

		case r.Method == http.MethodPost && r.URL.Path == "/1/clouddrive/share/sharepage/save":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["scene"] != "link" || body["to_pdir_fid"] != "dir-target" || body["stoken"] != "stk-001" {
				t.Fatalf("save 请求体字段异常: %+v", body)
			}
			writeEnvelope(w, 200, 0, "", map[string]interface{}{
				"task_id": "task-save-99",
				"finish":  false,
			})

		case r.Method == http.MethodGet && r.URL.Path == "/1/clouddrive/task":
			taskPollCount++
			if taskPollCount == 1 {
				writeEnvelope(w, 200, 0, "", map[string]interface{}{
					"task_id": "task-save-99",
					"status":  1,
				})
				return
			}
			writeEnvelope(w, 200, 0, "", map[string]interface{}{
				"task_id": "task-save-99",
				"status":  2,
				"save_as": map[string]interface{}{
					"save_as_top_fids": []string{"saved-fid-a", "saved-fid-b"},
				},
			})

		case r.Method == http.MethodGet && r.URL.Path == "/1/clouddrive/file/sort":
			sortCalls++
			listCalls++
			writeEnvelope(w, 200, 0, "", map[string]interface{}{
				"list": []map[string]interface{}{
					{"fid": "saved-fid-a", "pdir_fid": "dir-target", "file_name": "part-001.bin", "size": 1024},
				},
			})

		default:
			t.Fatalf("未预期请求: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	cli, err := NewClient(ClientOptions{
		DriveBaseURL: srv.URL,
		Cookie:       "__pus=ok; __puus=ok",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 预先缓存目标目录，用于验证 SaveShareFiles 后是否失效
	if _, err := cli.ListDirByFID(context.Background(), "dir-target", nil); err != nil {
		t.Fatal(err)
	}

	tok, err := cli.GetShareToken(context.Background(), "pwd123456", "1a2b")
	if err != nil {
		t.Fatalf("GetShareToken 失败: %v", err)
	}
	detail, err := cli.ListShareFiles(context.Background(), "pwd123456", tok.SToken, "")
	if err != nil {
		t.Fatalf("ListShareFiles 失败: %v", err)
	}
	if len(detail.Items) != 101 || detail.Total != 101 {
		t.Fatalf("分享翻页数量异常: len=%d total=%d", len(detail.Items), detail.Total)
	}
	if detail.Items[100].ShareFIDToken != "ftok-101" {
		t.Fatalf("share_fid_token 未正确解析: %+v", detail.Items[100])
	}

	saveRes, err := cli.SaveShareFiles(context.Background(), ShareSaveRequest{
		PWDID:     "pwd123456",
		SToken:    tok.SToken,
		PDirFID:   "0",
		ToPDirFID: "dir-target",
		FIDs:      []string{"sfid-001", "sfid-101"},
		FIDTokens: []string{"ftok-001", "ftok-101"},
	})
	if err != nil {
		t.Fatalf("SaveShareFiles 失败: %v", err)
	}
	if !reflect.DeepEqual(saveRes.SavedTopFIDs, []string{"saved-fid-a", "saved-fid-b"}) {
		t.Fatalf("save_as_top_fids 未正确解析: %+v", saveRes.SavedTopFIDs)
	}

	// 验证目标目录缓存被精准清除
	if _, err := cli.ListDirByFID(context.Background(), "dir-target", &ListOptions{}); err != nil {
		t.Fatal(err)
	}
	if sortCalls != 2 || listCalls != 2 {
		t.Fatalf("期望缓存失效后再次触发服务端拉取，实际 sortCalls=%d", sortCalls)
	}
}

func TestShareManageFlow(t *testing.T) {
	var canceledIDs []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/1/clouddrive/share":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if int(body["url_type"].(float64)) != 2 || int(body["expired_type"].(float64)) != 3 || body["passcode"] != "8x9y" {
				t.Fatalf("创建分享参数不符: %+v", body)
			}
			writeEnvelope(w, 200, 0, "", map[string]interface{}{
				"task_id": "task-share-1",
				"finish":  false,
			})

		case r.Method == http.MethodGet && r.URL.Path == "/1/clouddrive/task":
			writeEnvelope(w, 200, 0, "", map[string]interface{}{
				"task_id":  "task-share-1",
				"status":   2,
				"share_id": "sh-9988",
			})

		case r.Method == http.MethodPost && r.URL.Path == "/1/clouddrive/share/password":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["share_id"] != "sh-9988" {
				t.Fatalf("share/password 缺少 share_id: %+v", body)
			}
			writeEnvelope(w, 200, 0, "", map[string]interface{}{
				"share_id":     "sh-9988",
				"pwd_id":       "pwd9988",
				"title":        "讲义合集",
				"share_url":    "https://pan.quark.cn/s/pwd9988",
				"passcode":     "8x9y",
				"url_type":     2,
				"expired_type": 3,
				"expired_at":   1796000000000,
			})

		case r.Method == http.MethodGet && r.URL.Path == "/1/clouddrive/share/mypage/detail":
			_, _ = fmt.Fprint(w, `{"status":200,"code":0,"data":{"list":[{"share_id":"sh-9988","pwd_id":"pwd9988","title":"讲义合集","share_url":"https://pan.quark.cn/s/pwd9988","passcode":"8x9y","expired_type":3,"click_pv":12,"save_pv":3}]},"metadata":{"_total":1,"_page":1,"_size":50}}`)

		case r.Method == http.MethodPost && r.URL.Path == "/1/clouddrive/share/delete":
			var body struct {
				ShareIDs []string `json:"share_ids"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			canceledIDs = body.ShareIDs
			writeEnvelope(w, 200, 0, "", map[string]interface{}{})

		default:
			t.Fatalf("未预期请求: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	cli, err := NewClient(ClientOptions{
		DriveBaseURL: srv.URL,
		Cookie:       "__pus=ok; __puus=ok",
	})
	if err != nil {
		t.Fatal(err)
	}

	// 参数合法性校验
	if _, err := cli.CreateShare(context.Background(), CreateShareOptions{
		FIDs:       []string{"f1"},
		PeriodDays: 15,
	}); err == nil {
		t.Fatal("不支持的分享天数应当报错")
	}
	if _, err := cli.CreateShare(context.Background(), CreateShareOptions{
		FIDs:       []string{"f1"},
		PeriodDays: 7,
		Passcode:   "123",
	}); err == nil {
		t.Fatal("非 4 位提取码应当报错")
	}

	created, err := cli.CreateShare(context.Background(), CreateShareOptions{
		FIDs:       []string{"f1"},
		Title:      "讲义合集",
		Passcode:   "8x9y",
		PeriodDays: 7,
	})
	if err != nil {
		t.Fatalf("CreateShare 失败: %v", err)
	}
	if created.ShareID != "sh-9988" || created.ShareURL != "https://pan.quark.cn/s/pwd9988" {
		t.Fatalf("创建分享返回结果异常: %+v", created)
	}

	listRes, err := cli.ListMyShares(context.Background(), 1, 50)
	if err != nil {
		t.Fatalf("ListMyShares 失败: %v", err)
	}
	if listRes.Total != 1 || len(listRes.Items) != 1 || listRes.Items[0].ClickPV != 12 {
		t.Fatalf("我的分享列表解析异常: %+v", listRes)
	}

	if err := cli.CancelShares(context.Background(), []string{"sh-9988"}); err != nil {
		t.Fatalf("CancelShares 失败: %v", err)
	}
	if !reflect.DeepEqual(canceledIDs, []string{"sh-9988"}) {
		t.Fatalf("取消分享 ID 不符: %+v", canceledIDs)
	}
}

func TestRecycleFlow(t *testing.T) {
	var actions []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/1/clouddrive/file/recycle/list":
			_, _ = fmt.Fprint(w, `{"status":200,"code":0,"data":{"list":[{"record_id":"rec-1","fid":"fid-1","file_name":"废弃草稿.pdf","size":2048,"dir":false,"deleted_at":1795000000000,"expired_at":1795864000000}]},"metadata":{"_total":1,"_page":1,"_size":50}}`)

		case r.Method == http.MethodPost && r.URL.Path == "/1/clouddrive/file/recycle/restore":
			var body struct {
				SelectMode int      `json:"select_mode"`
				RecordList []string `json:"record_list"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.SelectMode != 2 || !reflect.DeepEqual(body.RecordList, []string{"rec-1"}) {
				t.Fatalf("restore 请求参数不符: %+v", body)
			}
			actions = append(actions, "restore")
			writeEnvelope(w, 200, 0, "", map[string]interface{}{
				"task_id": "task-restore-1",
				"finish":  false,
			})

		case r.Method == http.MethodGet && r.URL.Path == "/1/clouddrive/task":
			writeEnvelope(w, 200, 0, "", map[string]interface{}{
				"task_id": r.URL.Query().Get("task_id"),
				"status":  2,
			})

		case r.Method == http.MethodPost && r.URL.Path == "/1/clouddrive/file/recycle/remove":
			var body struct {
				SelectMode int      `json:"select_mode"`
				RecordList []string `json:"record_list"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.SelectMode == 2 && reflect.DeepEqual(body.RecordList, []string{"rec-1"}) {
				actions = append(actions, "remove_one")
			} else if body.SelectMode == 1 && len(body.RecordList) == 0 {
				actions = append(actions, "clear_all")
			} else {
				t.Fatalf("remove 请求参数不符: %+v", body)
			}
			writeEnvelope(w, 200, 0, "", map[string]interface{}{
				"finish": true,
			})

		default:
			t.Fatalf("未预期请求: %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()

	cli, err := NewClient(ClientOptions{
		DriveBaseURL: srv.URL,
		Cookie:       "__pus=ok; __puus=ok",
	})
	if err != nil {
		t.Fatal(err)
	}

	rcList, err := cli.ListRecycle(context.Background(), 1, 50)
	if err != nil {
		t.Fatalf("ListRecycle 失败: %v", err)
	}
	if rcList.Total != 1 || len(rcList.Items) != 1 || rcList.Items[0].RecordID != "rec-1" {
		t.Fatalf("回收站列表解析异常: %+v", rcList)
	}

	if err := cli.RestoreRecycle(context.Background(), []string{"rec-1"}); err != nil {
		t.Fatalf("RestoreRecycle 失败: %v", err)
	}
	if err := cli.DeleteRecycle(context.Background(), []string{"rec-1"}); err != nil {
		t.Fatalf("DeleteRecycle 失败: %v", err)
	}
	if err := cli.ClearRecycle(context.Background()); err != nil {
		t.Fatalf("ClearRecycle 失败: %v", err)
	}

	wantActions := []string{"restore", "remove_one", "clear_all"}
	if !reflect.DeepEqual(actions, wantActions) {
		t.Fatalf("回收站请求顺序不符: got=%+v want=%+v", actions, wantActions)
	}
}

func TestShareErrorClassification(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["pwd_id"] == "badpass" {
			writeEnvelope(w, 400, 41008, "提取码错误", nil)
			return
		}
		writeEnvelope(w, 404, 41004, "分享已失效或不存在", nil)
	}))
	defer srv.Close()

	cli, err := NewClient(ClientOptions{
		DriveBaseURL: srv.URL,
		Cookie:       "__pus=ok; __puus=ok",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = cli.GetShareToken(context.Background(), "badpass", "0000")
	if !errors.Is(err, ErrSharePasscodeInvalid) {
		t.Fatalf("期望命中 ErrSharePasscodeInvalid，实际: %v", err)
	}

	_, err = cli.GetShareToken(context.Background(), "expired", "")
	if !errors.Is(err, ErrShareInvalid) {
		t.Fatalf("期望命中 ErrShareInvalid，实际: %v", err)
	}
}

func mustJSON(t *testing.T, v interface{}) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeEnvelope(w http.ResponseWriter, status, code int, msg string, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	if status >= 400 && status < 600 {
		w.WriteHeader(status)
	}
	env := map[string]interface{}{
		"status":  status,
		"code":    code,
		"message": msg,
		"data":    data,
	}
	_ = json.NewEncoder(w).Encode(env)
}
