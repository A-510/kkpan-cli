package transfer

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash/crc64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kkpan-cli/kkpan/quarkpan"
)

func TestDownloaderChunkedResumeAndVerify(t *testing.T) {
	payload := bytes.Repeat([]byte("QUARK-ANIME-CHUNK-DATA-"), 2000) // ~46KB
	h := md5.Sum(payload)
	expectedMD5 := hex.EncodeToString(h[:])

	var failOnce atomic.Bool
	failOnce.Store(true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "" {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
			return
		}
		var start, end int
		if _, err := fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if end >= len(payload) {
			end = len(payload) - 1
		}
		// 模拟第 2 个分片首次请求中断，验证断点续传恢复
		if start > 0 && failOnce.CompareAndSwap(true, false) {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer srv.Close()

	cli, _ := quarkpan.NewClient(quarkpan.ClientOptions{Cookie: "__pus=ok"})
	dl := NewDownloader(cli)

	tmpDir := t.TempDir()
	dest := filepath.Join(tmpDir, "movie.bin")
	link := quarkpan.DownloadLink{
		FID:         "f1",
		FileName:    "movie.bin",
		Size:        int64(len(payload)),
		MD5:         expectedMD5,
		UpdatedAt:   1700000000,
		DownloadURL: srv.URL + "/dl",
	}

	// 第一次：maxRetry=0，故意中途失败，留下一部分已下载分片状态
	_, err := dl.DownloadFile(context.Background(), link, dest, DownloadConfig{
		Parallel:  3,
		ChunkSize: 10240,
		MaxRetry:  0,
	})
	if err == nil {
		t.Fatalf("期望首次因模拟故障而失败")
	}
	if _, err := os.Stat(dest + downloadStateSuffix); err != nil {
		t.Fatalf("期望生成断点续传状态文件: %v", err)
	}

	// 第二次：续传完成剩余分片并校验 MD5
	res, err := dl.DownloadFile(context.Background(), link, dest, DownloadConfig{
		Parallel:    3,
		ChunkSize:   10240,
		MaxRetry:    1,
		ModifyMTime: true,
	})
	if err != nil {
		t.Fatalf("断点续传失败: %v", err)
	}
	if !res.Verified || res.Size != int64(len(payload)) {
		t.Fatalf("下载结果校验异常: %+v", res)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("拼接后文件内容与源文件不一致")
	}
	if _, err := os.Stat(dest + downloadStateSuffix); !os.IsNotExist(err) {
		t.Fatalf("完成后应清理状态文件")
	}
}

func TestRateLimiterSpeedControl(t *testing.T) {
	limiter := NewRateLimiter(40 * 1024) // 40KB/s
	data := bytes.Repeat([]byte("A"), 80*1024)
	r := NewLimitedReader(context.Background(), bytes.NewReader(data), limiter)
	start := time.Now()
	_, err := io.ReadAll(r)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("LimitedReader 读取失败: %v", err)
	}
	// 初始桶容 40KB + 后续 40KB，约需 >= 0.75s
	if elapsed < 700*time.Millisecond {
		t.Fatalf("限速器未生效，耗时过短: %v", elapsed)
	}
}

func TestUploaderConflictPoliciesAndMultipart(t *testing.T) {
	tmpDir := t.TempDir()
	localPath := filepath.Join(tmpDir, "notes.txt")
	content := []byte("0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ")
	if err := os.WriteFile(localPath, content, 0644); err != nil {
		t.Fatal(err)
	}

	md5H := md5.Sum(content)
	sha1H := sha1.Sum(content)
	if m, s, sz, err := HashFileSinglePass(localPath); err != nil || sz != int64(len(content)) || m != hex.EncodeToString(md5H[:]) || s != hex.EncodeToString(sha1H[:]) {
		t.Fatalf("HashFileSinglePass 校验失败: m=%s s=%s sz=%d err=%v", m, s, sz, err)
	}

	var mu sync.Mutex
	uploadedParts := make(map[string][]byte)
	var lastPreName string
	var deletedOld atomic.Bool

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/1/clouddrive/file/sort":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": 200, "code": 0,
				"data": map[string]interface{}{
					"list": []quarkpan.FileItem{{FID: "old-fid", PDirFID: "0", FileName: "notes.txt", Size: 5}},
				},
			})
		case "/1/clouddrive/file/delete":
			deletedOld.Store(true)
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"task_id":"del-1","finish":true}}`))
		case "/1/clouddrive/file/upload/pre":
			var body map[string]interface{}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if ccpUpdate, _ := body["ccp_hash_update"].(bool); !ccpUpdate {
				if md5Val, _ := body["md5"].(string); md5Val == "" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"status":400,"code":14001,"message":"Bad Parameter: [md5 is null!]"}`))
					return
				}
			}
			if ft, _ := body["format_type"].(string); ft != "text/plain" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, `{"status":400,"code":14001,"message":"Bad Parameter: [unexpected format_type=%s]"}`, ft)
				return
			}
			mu.Lock()
			lastPreName, _ = body["file_name"].(string)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"status": 200, "code": 0,
				"data": map[string]interface{}{
					"task_id":    "t-up",
					"upload_id":  "u-id",
					"obj_key":    "k/notes.txt",
					"upload_url": srv.URL + "/oss",
					"bucket":     "bkt",
					"auth_info":  "ai",
				},
				"metadata": map[string]interface{}{"part_size": 10},
			})
		case "/1/clouddrive/file/update/hash":
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"finish":false}}`))
		case "/1/clouddrive/file/upload/auth":
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"auth_key":"OSS ok"}}`))
		case "/oss/k/notes.txt":
			if r.Method == http.MethodPut {
				p := r.URL.Query().Get("partNumber")
				b, _ := io.ReadAll(r.Body)
				mu.Lock()
				uploadedParts[p] = b
				mu.Unlock()
				w.Header().Set("ETag", `"`+p+`"`)
			}
			w.WriteHeader(http.StatusOK)
		case "/1/clouddrive/file/upload/finish":
			_, _ = w.Write([]byte(`{"status":200,"code":0,"data":{"fid":"new-fid-1"}}`))
		}
	}))
	defer srv.Close()

	cli, _ := quarkpan.NewClient(quarkpan.ClientOptions{DriveBaseURL: srv.URL, Cookie: "__pus=ok"})
	up := NewUploader(cli)

	// 1. skip 策略应跳过
	resSkip, err := up.UploadFile(context.Background(), localPath, "0", UploadConfig{Policy: "skip"})
	if err != nil || !resSkip.Skipped {
		t.Fatalf("skip 策略异常: %+v err=%v", resSkip, err)
	}

	// 2. rename 策略应改名为 notes (1).txt 并并发分片上传完成
	resRen, err := up.UploadFile(context.Background(), localPath, "0", UploadConfig{Policy: "rename", Parallel: 2})
	if err != nil || resRen.Skipped || resRen.RemoteName != "notes (1).txt" || resRen.FID != "new-fid-1" {
		t.Fatalf("rename 上传异常: %+v err=%v", resRen, err)
	}
	if lastPreName != "notes (1).txt" {
		t.Fatalf("预期上传文件名 notes (1).txt，实际: %s", lastPreName)
	}

	// 验证 OSS 收齐并正确拼接所有分片
	var reassembled bytes.Buffer
	for p := 1; p <= 4; p++ {
		reassembled.Write(uploadedParts[strconv.Itoa(p)])
	}
	if !bytes.Equal(reassembled.Bytes(), content) {
		t.Fatalf("分片上传拼接内容不一致: got=%q", reassembled.String())
	}

	// 3. overwrite 策略应先删旧文件再上传
	resOw, err := up.UploadFile(context.Background(), localPath, "0", UploadConfig{Policy: "overwrite", Parallel: 2})
	if err != nil || !deletedOld.Load() || resOw.RemoteName != "notes.txt" {
		t.Fatalf("overwrite 上传异常: %+v deletedOld=%v err=%v", resOw, deletedOld.Load(), err)
	}
}

func TestDownloaderFallbackToBackupURLOnEOF(t *testing.T) {
	payload := []byte("BACKUP-CDN-PAYLOAD-OK-1234567890")
	sum := md5.Sum(payload)
	expectedMD5 := hex.EncodeToString(sum[:])

	var primaryHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/primary" {
			primaryHits.Add(1)
			// 强制劫持并立即关闭 TCP 连接，真实模拟服务端断连 EOF
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, err := hj.Hijack()
				if err == nil {
					_ = conn.Close()
					return
				}
			}
			return
		}
		if r.URL.Path == "/backup" {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	cli, _ := quarkpan.NewClient(quarkpan.ClientOptions{Cookie: "__pus=ok"})
	dl := NewDownloader(cli)
	dest := filepath.Join(t.TempDir(), "fallback.bin")

	res, err := dl.DownloadFile(context.Background(), quarkpan.DownloadLink{
		FID:         "f-backup",
		FileName:    "fallback.bin",
		Size:        int64(len(payload)),
		MD5:         expectedMD5,
		DownloadURL: srv.URL + "/primary",
		BackupURL:   srv.URL + "/backup",
	}, dest, DownloadConfig{Parallel: 1, MaxRetry: 1})
	if err != nil {
		t.Fatalf("主链接 EOF 后应自动回退到 BackupURL 成功，实际报错: %v", err)
	}
	if primaryHits.Load() < 1 || !res.Verified {
		t.Fatalf("主链接命中次数或校验状态异常: hits=%d res=%+v", primaryHits.Load(), res)
	}
	got, err := os.ReadFile(dest)
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("回退下载的文件内容不一致")
	}
}

func TestDownloaderSingleThreadSkipsProbeAndNoDuplicateCookie(t *testing.T) {
	payload := []byte("SINGLE-STREAM-WITHOUT-PROBE")
	var reqCount atomic.Int32
	var sawProbeRange atomic.Bool
	var cookieHeaderCount int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCount.Add(1)
		if r.Header.Get("Range") == "bytes=0-0" {
			sawProbeRange.Store(true)
		}
		cookieHeaderCount = len(r.Header.Values("Cookie"))
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	cli, _ := quarkpan.NewClient(quarkpan.ClientOptions{Cookie: "__pus=ok; __puus=u2"})
	dl := NewDownloader(cli)
	dest := filepath.Join(t.TempDir(), "single.bin")

	_, err := dl.DownloadFile(context.Background(), quarkpan.DownloadLink{
		FID:         "f-single",
		FileName:    "single.bin",
		Size:        int64(len(payload)),
		DownloadURL: srv.URL + "/dl",
	}, dest, DownloadConfig{Parallel: 1, MaxRetry: 0, NoCheck: true})
	if err != nil {
		t.Fatalf("单线程下载失败: %v", err)
	}
	if reqCount.Load() != 1 || sawProbeRange.Load() {
		t.Fatalf("单线程已知体积应跳过 Range 0-0 探测，实际请求数=%d sawProbe=%v", reqCount.Load(), sawProbeRange.Load())
	}
	if cookieHeaderCount != 1 {
		t.Fatalf("期望只有 1 个 Cookie 请求头，实际=%d", cookieHeaderCount)
	}
}

func TestDownloaderToleratesQuarkNonStandardAPIMD5WhenSizeMatches(t *testing.T) {
	payload := bytes.Repeat([]byte("QUARK-PDF-STREAM-BLOCK-"), 2000) // ~46KB
	wrongAPIMD5 := "1656c3c2e01632b14b720ddc3946b1ef"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "" {
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			_, _ = w.Write(payload)
			return
		}
		var start, end int
		if _, err := fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if end >= len(payload) {
			end = len(payload) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer srv.Close()

	cli, _ := quarkpan.NewClient(quarkpan.ClientOptions{Cookie: "__pus=ok"})
	dl := NewDownloader(cli)

	for _, tc := range []struct {
		name     string
		parallel int
	}{
		{name: "single_stream", parallel: 1},
		{name: "chunked_multi", parallel: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), tc.name+".pdf")
			res, err := dl.DownloadFile(context.Background(), quarkpan.DownloadLink{
				FID:         "f-" + tc.name,
				FileName:    tc.name + ".pdf",
				Size:        int64(len(payload)),
				MD5:         wrongAPIMD5,
				DownloadURL: srv.URL + "/dl",
			}, dest, DownloadConfig{
				Parallel:  tc.parallel,
				ChunkSize: 10240,
				MaxRetry:  1,
			})
			if err != nil {
				t.Fatalf("体积完整但控制面 MD5 不一致时应允许完成，实际报错: %v", err)
			}
			if !res.MD5Mismatch || res.Verified || res.Size != int64(len(payload)) {
				t.Fatalf("期望 MD5Mismatch=true Verified=false Size=%d，实际: %+v", len(payload), res)
			}
			got, err := os.ReadFile(dest)
			if err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("最终落盘内容不一致")
			}
			if _, err := os.Stat(dest + downloadingSuffix); !os.IsNotExist(err) {
				t.Fatalf("不应残留临时文件")
			}
		})
	}
}

func TestDownloaderNormalizesBase64APIMD5(t *testing.T) {
	payload := []byte("BASE64-MD5-COMPAT-PAYLOAD-0123456789")
	sum := md5.Sum(payload)
	b64MD5 := base64.StdEncoding.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	cli, _ := quarkpan.NewClient(quarkpan.ClientOptions{Cookie: "__pus=ok"})
	dl := NewDownloader(cli)
	dest := filepath.Join(t.TempDir(), "b64.bin")

	res, err := dl.DownloadFile(context.Background(), quarkpan.DownloadLink{
		FID:         "f-b64",
		FileName:    "b64.bin",
		Size:        int64(len(payload)),
		MD5:         b64MD5,
		DownloadURL: srv.URL + "/dl",
	}, dest, DownloadConfig{Parallel: 1, MaxRetry: 0})
	if err != nil {
		t.Fatalf("Base64 格式 MD5 校验失败: %v", err)
	}
	if !res.Verified || res.MD5Mismatch {
		t.Fatalf("期望 Verified=true MD5Mismatch=false，实际: %+v", res)
	}
}

func TestDownloaderRejectsTruncatedSingleStream(t *testing.T) {
	fullLen := 4096
	truncated := bytes.Repeat([]byte("T"), 1024)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(truncated)))
		_, _ = w.Write(truncated)
	}))
	defer srv.Close()

	cli, _ := quarkpan.NewClient(quarkpan.ClientOptions{Cookie: "__pus=ok"})
	dl := NewDownloader(cli)
	dest := filepath.Join(t.TempDir(), "truncated.bin")

	_, err := dl.DownloadFile(context.Background(), quarkpan.DownloadLink{
		FID:         "f-trunc",
		FileName:    "truncated.bin",
		Size:        int64(fullLen),
		DownloadURL: srv.URL + "/dl",
	}, dest, DownloadConfig{Parallel: 1, MaxRetry: 0})
	if err == nil {
		t.Fatalf("截断流应返回长度不完整错误")
	}
	if _, statErr := os.Stat(dest); !os.IsNotExist(statErr) {
		t.Fatalf("截断下载不应生成最终文件")
	}
}

func TestDownloaderEnforcesTransportContentMD5AndCRC64(t *testing.T) {
	payload := []byte("OSS-TRANSPORT-INTEGRITY-CHECK-DATA-9876543210")
	sum := md5.Sum(payload)
	validB64MD5 := base64.StdEncoding.EncodeToString(sum[:])
	wrongB64MD5 := base64.StdEncoding.EncodeToString(make([]byte, md5.Size))

	crcTable := crc64.MakeTable(crc64.ECMA)
	validCRC := crc64.Checksum(payload, crcTable)

	cli, _ := quarkpan.NewClient(quarkpan.ClientOptions{Cookie: "__pus=ok"})
	dl := NewDownloader(cli)

	t.Run("valid_crc64_with_mismatched_api_md5", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.Header().Set("x-oss-hash-crc64ecma", strconv.FormatUint(validCRC, 10))
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		dest := filepath.Join(t.TempDir(), "ok_crc.bin")
		res, err := dl.DownloadFile(context.Background(), quarkpan.DownloadLink{
			FID:         "f-crc-ok",
			FileName:    "ok_crc.bin",
			Size:        int64(len(payload)),
			MD5:         "00000000000000000000000000000000",
			DownloadURL: srv.URL + "/dl",
		}, dest, DownloadConfig{Parallel: 1, MaxRetry: 0})
		if err != nil {
			t.Fatalf("合法 CRC64 应通过校验: %v", err)
		}
		if !res.Verified || !res.MD5Mismatch {
			t.Fatalf("期望 Verified=true MD5Mismatch=true，实际: %+v", res)
		}
	})

	t.Run("invalid_crc64_fails_hard", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.Header().Set("x-oss-hash-crc64ecma", strconv.FormatUint(validCRC+1, 10))
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		dest := filepath.Join(t.TempDir(), "bad_crc.bin")
		_, err := dl.DownloadFile(context.Background(), quarkpan.DownloadLink{
			FID:         "f-crc-bad",
			FileName:    "bad_crc.bin",
			Size:        int64(len(payload)),
			DownloadURL: srv.URL + "/dl",
		}, dest, DownloadConfig{Parallel: 1, MaxRetry: 0})
		if err == nil {
			t.Fatalf("错误的 x-oss-hash-crc64ecma 应阻断落盘")
		}
	})

	t.Run("invalid_content_md5_fails_hard", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.Header().Set("Content-MD5", wrongB64MD5)
			_, _ = w.Write(payload)
		}))
		defer srv.Close()

		dest := filepath.Join(t.TempDir(), "bad_cmd5.bin")
		_, err := dl.DownloadFile(context.Background(), quarkpan.DownloadLink{
			FID:         "f-cmd5-bad",
			FileName:    "bad_cmd5.bin",
			Size:        int64(len(payload)),
			MD5:         validB64MD5,
			DownloadURL: srv.URL + "/dl",
		}, dest, DownloadConfig{Parallel: 1, MaxRetry: 0})
		if err == nil {
			t.Fatalf("错误的 Content-MD5 响应头应阻断落盘")
		}
	})
}

func TestDownloaderRejectsNon206ForNonZeroRange(t *testing.T) {
	payload := bytes.Repeat([]byte("RANGE-DOWNGRADE-GUARD-"), 1500) // ~33KB
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rangeHdr := r.Header.Get("Range")
		if rangeHdr == "bytes=0-0" {
			w.Header().Set("Content-Range", fmt.Sprintf("bytes 0-0/%d", len(payload)))
			w.Header().Set("Content-Length", "1")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write(payload[:1])
			return
		}
		var start, end int
		if _, err := fmt.Sscanf(rangeHdr, "bytes=%d-%d", &start, &end); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if start > 0 {
			// 模拟 CDN 忽略非 0 Range 并错误返回 200 OK 全量流
			w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(payload)
			return
		}
		if end >= len(payload) {
			end = len(payload) - 1
		}
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(payload)))
		w.Header().Set("Content-Length", strconv.Itoa(end-start+1))
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(payload[start : end+1])
	}))
	defer srv.Close()

	cli, _ := quarkpan.NewClient(quarkpan.ClientOptions{Cookie: "__pus=ok"})
	dl := NewDownloader(cli)
	dest := filepath.Join(t.TempDir(), "range_guard.bin")

	_, err := dl.DownloadFile(context.Background(), quarkpan.DownloadLink{
		FID:         "f-range-guard",
		FileName:    "range_guard.bin",
		Size:        int64(len(payload)),
		DownloadURL: srv.URL + "/dl",
	}, dest, DownloadConfig{Parallel: 3, ChunkSize: 10240, MaxRetry: 0})
	if err == nil {
		t.Fatalf("非 0 偏移收到 200 OK 时应返回错误而非写花文件")
	}
}
