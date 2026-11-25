package quarkpan

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCookieAutoRefreshCallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "__puus", Value: "refreshed_puus_val", Path: "/"})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"member_type":"SVIP","use_capacity":1024,"total_capacity":65536}}`))
	}))
	defer srv.Close()

	var updated string
	cli, err := NewClient(ClientOptions{
		DriveBaseURL: srv.URL,
		Cookie:       "__pus=init_pus; __puus=old_puus",
		OnCookieUpdated: func(cookieHeader string) {
			updated = cookieHeader
		},
	})
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}

	quota, err := cli.GetMemberQuota(context.Background())
	if err != nil {
		t.Fatalf("GetMemberQuota 失败: %v", err)
	}
	if !quota.IsSVIP || quota.TotalCapacity != 65536 {
		t.Fatalf("配额解析异常: %+v", quota)
	}
	if !strings.Contains(updated, "__puus=refreshed_puus_val") || !strings.Contains(updated, "__pus=init_pus") {
		t.Fatalf("Cookie 自动刷新回调未正确合并: %q", updated)
	}
}

func TestQRLoginThreeStatesAndExchange(t *testing.T) {
	var pollCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/cas/ajax/getTokenForQrcodeLogin":
			_, _ = w.Write([]byte(`{"status":2000000,"code":0,"data":{"members":{"token":"qr-tok-001"}}}`))
		case "/cas/ajax/getServiceTicketByQrcodeToken":
			n := pollCount.Add(1)
			if n == 1 {
				_, _ = w.Write([]byte(`{"status":50004001,"message":"waiting"}`))
				return
			}
			if r.URL.Query().Get("token") == "expired-tok" {
				_, _ = w.Write([]byte(`{"status":50004002,"message":"expired"}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":2000000,"data":{"members":{"service_ticket":"st-ok-999"}}}`))
		case "/account/info":
			if r.URL.Query().Get("st") != "st-ok-999" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "__pus", Value: "pus-val", Path: "/"})
			http.SetCookie(w, &http.Cookie{Name: "__puus", Value: "puus-val", Path: "/"})
			_, _ = w.Write([]byte(`{"success":true,"data":{"nickname":"星穹旅人","uid":"8848123"}}`))
		case "/1/clouddrive/member":
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"member_type":"VIP","use_capacity":100,"total_capacity":2000}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cli, err := NewClient(ClientOptions{
		DriveBaseURL: srv.URL,
		PanBaseURL:   srv.URL,
		UOPBaseURL:   srv.URL,
	})
	if err != nil {
		t.Fatalf("NewClient 失败: %v", err)
	}

	sess, err := cli.GetQRToken(context.Background())
	if err != nil {
		t.Fatalf("GetQRToken 失败: %v", err)
	}
	if sess.Token != "qr-tok-001" || !strings.Contains(sess.QRURL, "token=qr-tok-001") {
		t.Fatalf("QR 会话内容异常: %+v", sess)
	}

	res, err := cli.RunQRLoginLoop(context.Background(), sess, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("RunQRLoginLoop 失败: %v", err)
	}
	if res.Account.Nickname != "星穹旅人" || res.Account.UID != "8848123" {
		t.Fatalf("账号信息异常: %+v", res.Account)
	}
	if !strings.Contains(res.Cookie, "__pus=pus-val") || !strings.Contains(res.Cookie, "__puus=puus-val") {
		t.Fatalf("换票返回的 Cookie 缺失字段: %q", res.Cookie)
	}

	// 验证二维码过期错误映射
	_, err = cli.PollQRStatus(context.Background(), "expired-tok", sess.RequestID)
	if !errors.Is(err, ErrQRExpired) {
		t.Fatalf("期望返回 ErrQRExpired，实际: %v", err)
	}
}

func TestPathResolverPaginationAndCRUD(t *testing.T) {
	var listCalls atomic.Int32
	var taskPolls atomic.Int32
	var mu sync.Mutex
	createdDir := false
	renamed := false

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/1/clouddrive/file/sort":
			listCalls.Add(1)
			pdir := r.URL.Query().Get("pdir_fid")
			page, _ := strconv.Atoi(r.URL.Query().Get("_page"))
			switch pdir {
			case "0":
				if page == 1 {
					// 返回满 100 条以触发自动翻页
					items := make([]FileItem, 0, 100)
					items = append(items, FileItem{FID: "dir-anime", PDirFID: "0", FileName: "番剧资料", IsDir: true})
					for i := 1; i < 100; i++ {
						items = append(items, FileItem{FID: fmt.Sprintf("f-%03d", i), PDirFID: "0", FileName: fmt.Sprintf("item-%03d.txt", i), Size: int64(i)})
					}
					_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": 200, "code": 0, "data": map[string]interface{}{"list": items}})
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{
					"status": 200, "code": 0,
					"data": map[string]interface{}{"list": []FileItem{{FID: "f-last", PDirFID: "0", FileName: "尾页文件.txt", Size: 999}}},
				})
			case "dir-anime":
				mu.Lock()
				hasNew := createdDir
				isRenamed := renamed
				mu.Unlock()
				list := []FileItem{{FID: "file-ep1", PDirFID: "dir-anime", FileName: "EP01.mkv", Size: 2048, MD5: "abc"}}
				if isRenamed {
					list[0].FileName = "EP01_BD.mkv"
				}
				if hasNew {
					list = append(list, FileItem{FID: "dir-sub", PDirFID: "dir-anime", FileName: "字幕", IsDir: true})
				}
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": 200, "code": 0, "data": map[string]interface{}{"list": list}})
			default:
				_ = json.NewEncoder(w).Encode(map[string]interface{}{"status": 200, "code": 0, "data": map[string]interface{}{"list": []FileItem{}}})
			}
		case "/1/clouddrive/file":
			mu.Lock()
			createdDir = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"fid":"dir-sub"}}`))
		case "/1/clouddrive/file/rename":
			mu.Lock()
			renamed = true
			mu.Unlock()
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{}}`))
		case "/1/clouddrive/file/move":
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"task-mv-1","finish":false}}`))
		case "/1/clouddrive/file/delete":
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"task-del-fail","finish":false}}`))
		case "/1/clouddrive/task":
			tid := r.URL.Query().Get("task_id")
			if tid == "task-del-fail" {
				_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"task-del-fail","status":3,"message":"权限不足"}}`))
				return
			}
			if taskPolls.Add(1) == 1 {
				_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"task-mv-1","status":1}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"task-mv-1","status":2}}`))
		case "/1/clouddrive/file/download":
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":[{"fid":"file-ep1","file_name":"EP01.mkv","size":2048,"download_url":"https://cdn.example.com/ep1"}]}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cli, _ := NewClient(ClientOptions{DriveBaseURL: srv.URL, Cookie: "__pus=ok"})
	ctx := context.Background()

	// 验证自动翻页（100 + 1 = 101 条）
	rootItems, err := cli.ListDir(ctx, "/", nil)
	if err != nil {
		t.Fatalf("ListDir 根目录失败: %v", err)
	}
	if len(rootItems) != 101 {
		t.Fatalf("期望 101 条记录（含翻页），实际: %d", len(rootItems))
	}

	// 验证 ResolvePath 缓存命中与 fid: 直连
	beforeCalls := listCalls.Load()
	item, err := cli.Resolver().ResolvePath(ctx, "/番剧资料/EP01.mkv")
	if err != nil || item.FID != "file-ep1" {
		t.Fatalf("解析多级路径失败: item=%+v err=%v", item, err)
	}
	// 再次查询同一路径应命中缓存（listCalls 只增加解析番剧资料那一次）
	_, _ = cli.Resolver().ResolvePath(ctx, "/番剧资料/EP01.mkv")
	if got := listCalls.Load(); got != beforeCalls+1 {
		t.Fatalf("路径缓存未命中，预期调用增加 1 次，实际增加 %d 次", got-beforeCalls)
	}

	fidItem, err := cli.Resolver().ResolvePath(ctx, "fid:custom-99")
	if err != nil || fidItem.FID != "custom-99" {
		t.Fatalf("fid: 直连解析失败: %+v err=%v", fidItem, err)
	}

	// 验证写操作触发缓存失效
	newFID, err := cli.MkdirAll(ctx, "/番剧资料/字幕")
	if err != nil || newFID != "dir-sub" {
		t.Fatalf("MkdirAll 失败: fid=%s err=%v", newFID, err)
	}
	if err := cli.Rename(ctx, "file-ep1", "EP01_BD.mkv"); err != nil {
		t.Fatalf("Rename 失败: %v", err)
	}
	itemAfter, err := cli.Resolver().ResolvePath(ctx, "/番剧资料/EP01_BD.mkv")
	if err != nil || itemAfter.FID != "file-ep1" {
		t.Fatalf("Rename 后应失效缓存并能解析新文件名，实际: %+v err=%v", itemAfter, err)
	}

	// 验证异步 Move 轮询成功 & Delete 轮询失败映射
	if err := cli.Move(ctx, []string{"file-ep1"}, "dir-sub"); err != nil {
		t.Fatalf("Move 异步任务失败: %v", err)
	}
	if err := cli.Delete(ctx, []string{"file-ep1"}); !errors.Is(err, ErrTaskFailed) {
		t.Fatalf("期望 Delete 返回 ErrTaskFailed，实际: %v", err)
	}

	links, err := cli.GetDownloadLinks(ctx, []string{"file-ep1"})
	if err != nil || len(links) != 1 || links[0].DownloadURL != "https://cdn.example.com/ep1" {
		t.Fatalf("GetDownloadLinks 异常: %+v err=%v", links, err)
	}
}

func TestUploadRapidAndOSSMultipart(t *testing.T) {
	var ossParts sync.Map
	var ossCompleted atomic.Bool
	var callbackHeader string
	var contentMD5Header string
	var completeBody string
	var partReferer string
	var completeReferer string

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/1/clouddrive/file/upload/pre":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			if ccpUpdate, _ := body["ccp_hash_update"].(bool); !ccpUpdate {
				if md5Val, _ := body["md5"].(string); strings.TrimSpace(md5Val) == "" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"status":400,"code":14001,"message":"Bad Parameter: [md5 is null!]"}`))
					return
				}
			}
			if ft, _ := body["format_type"].(string); strings.Contains(ft, ";") {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"status":400,"code":14001,"message":"Bad Parameter: [format_type invalid]"}`))
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": 200, "code": 0,
				"data": map[string]interface{}{
					"task_id":    "up-task-1",
					"upload_id":  "oss-up-1",
					"obj_key":    "user/obj.bin",
					"upload_url": srv.URL + "/oss",
					"bucket":     "quark-bucket",
					"auth_info":  "auth-info-xyz",
					"callback":   map[string]string{"callbackUrl": "https://cb.example.com", "callbackBody": "ok"},
				},
				"metadata": map[string]interface{}{"part_size": 5},
			})
		case "/1/clouddrive/file/update/hash":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			w.Header().Set("Content-Type", "application/json")
			if body["md5"] == "rapid-md5" {
				_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"finish":true,"fid":"rapid-fid-1"}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"finish":false}}`))
		case "/1/clouddrive/file/upload/auth":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"auth_key":"OSS sig-ok"}}`))
		case "/oss/user/obj.bin":
			if r.Method == http.MethodPut {
				partNum := r.URL.Query().Get("partNumber")
				partReferer = r.Header.Get("Referer")
				data, _ := io.ReadAll(r.Body)
				ossParts.Store(partNum, string(data))
				w.Header().Set("ETag", `"ETAG-`+partNum+`"`)
				w.WriteHeader(http.StatusOK)
				return
			}
			if r.Method == http.MethodPost {
				callbackHeader = r.Header.Get("x-oss-callback")
				contentMD5Header = r.Header.Get("Content-MD5")
				completeReferer = r.Header.Get("Referer")
				rawXML, _ := io.ReadAll(r.Body)
				completeBody = string(rawXML)
				ossCompleted.Store(true)
				w.WriteHeader(http.StatusOK)
				return
			}
		case "/1/clouddrive/file/upload/finish":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"fid":"final-fid-888"}}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	cli, _ := NewClient(ClientOptions{DriveBaseURL: srv.URL, Cookie: "__pus=ok"})
	ctx := context.Background()

	pre, err := cli.UploadPre(ctx, "0", "demo.bin", "text/plain; charset=utf-8", 10, 0)
	if err != nil || pre.PartSize != 5 {
		t.Fatalf("UploadPre 失败: pre=%+v err=%v", pre, err)
	}
	if got := normalizeUploadMIME("text/plain; charset=utf-8"); got != "text/plain" {
		t.Fatalf("normalizeUploadMIME 预期 text/plain，实际: %q", got)
	}

	// 分支 1：秒传命中
	ok, rapidFID, err := cli.UploadUpdateHash(ctx, pre.TaskID, "rapid-md5", "rapid-sha1")
	if err != nil || !ok || rapidFID != "rapid-fid-1" {
		t.Fatalf("秒传分支失败: ok=%v fid=%s err=%v", ok, rapidFID, err)
	}

	// 分支 2：未命中走 OSS 分片上传 + 合并 + Finish
	ok2, _, err := cli.UploadUpdateHash(ctx, pre.TaskID, "normal-md5", "normal-sha1")
	if err != nil || ok2 {
		t.Fatalf("普通上传分支预期不命中秒传: ok=%v err=%v", ok2, err)
	}
	etag1, err := cli.UploadOSSPart(ctx, pre, 1, bytes.NewReader([]byte("hello")), 5, "application/octet-stream")
	if err != nil || etag1 != "ETAG-1" {
		t.Fatalf("UploadOSSPart 1 失败: etag=%s err=%v", etag1, err)
	}
	etag2, err := cli.UploadOSSPart(ctx, pre, 2, bytes.NewReader([]byte("world")), 5, "application/octet-stream")
	if err != nil || etag2 != "ETAG-2" {
		t.Fatalf("UploadOSSPart 2 失败: etag=%s err=%v", etag2, err)
	}
	if err := cli.CompleteOSSMultipart(ctx, pre, []UploadPart{
		{PartNumber: 2, ETag: etag2},
		{PartNumber: 1, ETag: etag1},
	}); err != nil {
		t.Fatalf("CompleteOSSMultipart 失败: %v", err)
	}
	if !ossCompleted.Load() || contentMD5Header == "" {
		t.Fatalf("OSS 合并未被调用或缺少 Content-MD5")
	}
	if partReferer != DefaultReferer || completeReferer != DefaultReferer {
		t.Fatalf("OSS 请求缺少 Referer=%q: part=%q complete=%q", DefaultReferer, partReferer, completeReferer)
	}
	if !strings.Contains(completeBody, `<?xml version="1.0" encoding="UTF-8"?>`) ||
		!strings.Contains(completeBody, `<ETag>"ETAG-1"</ETag>`) ||
		!strings.Contains(completeBody, `<ETag>"ETAG-2"</ETag>`) ||
		strings.Contains(completeBody, `&#34;`) {
		t.Fatalf("CompleteMultipartUpload XML 格式不符合预期: %s", completeBody)
	}
	cbDecoded, err := base64.StdEncoding.DecodeString(callbackHeader)
	if err != nil || !strings.Contains(string(cbDecoded), "callbackUrl") {
		t.Fatalf("x-oss-callback 编码异常: %q err=%v", callbackHeader, err)
	}
	finalFID, err := cli.UploadFinish(ctx, pre.ObjKey, pre.TaskID, "0")
	if err != nil || finalFID != "final-fid-888" {
		t.Fatalf("UploadFinish 失败: fid=%s err=%v", finalFID, err)
	}
}
