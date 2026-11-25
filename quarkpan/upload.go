package quarkpan

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	// DefaultUploadPartSize 当服务端未指定分片大小时的默认 4MB 分片体积
	DefaultUploadPartSize int64 = 4 * 1024 * 1024
	ossDefaultUA                = "aliyun-sdk-js/6.6.1 Chrome 100.0.4896.160 on Windows 10 64-bit"
)

// normalizeUploadMIME 规范化上传 MIME 类型，剥离 charset 等附加参数，缺失时回退为 application/octet-stream
func normalizeUploadMIME(mimeType string) string {
	trimmed := strings.TrimSpace(mimeType)
	if trimmed == "" {
		return "application/octet-stream"
	}
	mediaType, _, err := mime.ParseMediaType(trimmed)
	if err != nil || strings.TrimSpace(mediaType) == "" {
		return "application/octet-stream"
	}
	return mediaType
}

// UploadPre 初始化上传任务并从服务端获取 OSS 上传凭证、对象键及建议分片大小
func (c *Client) UploadPre(ctx context.Context, pdirFID, fileName, mimeType string, size, mtimeMs int64) (*UploadPreResponse, error) {
	if strings.TrimSpace(pdirFID) == "" {
		pdirFID = RootFID
	}
	mimeType = normalizeUploadMIME(mimeType)
	nowMs := time.Now().UnixMilli()
	if mtimeMs <= 0 {
		mtimeMs = nowMs
	}

	// NOTE: 必须携带 ccp_hash_update=true，否则服务端会在 pre 阶段强校验 md5 并返回 14001: [md5 is null!]
	req := map[string]interface{}{
		"ccp_hash_update": true,
		"dir_name":        "",
		"file_name":       fileName,
		"format_type":     mimeType,
		"l_created_at":    mtimeMs,
		"l_updated_at":    mtimeMs,
		"pdir_fid":        pdirFID,
		"size":            size,
	}

	// 夸克 pre 接口除 data 外可能附带 metadata.part_size，因此用自定义结构兼容两处字段
	if !c.HasCookie() {
		return nil, ErrNotLoggedIn
	}
	fullURL := c.buildDriveURL("/1/clouddrive/file/upload/pre", nil)

	rawReq, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("预上传序列化失败: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, fullURL, bytes.NewReader(rawReq))
	if err != nil {
		return nil, fmt.Errorf("预上传创建请求失败: %w", err)
	}
	httpReq.Header.Set("User-Agent", c.userAgent)
	httpReq.Header.Set("Accept", "application/json, text/plain, */*")
	httpReq.Header.Set("Content-Type", "application/json;charset=UTF-8")
	httpReq.Header.Set("Referer", DefaultReferer)
	httpReq.Header.Set("Origin", DefaultOrigin)
	httpReq.Header.Set("Cookie", c.CookieHeader())

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("预上传请求失败: %w", err)
	}
	defer resp.Body.Close()
	c.mergeResponseCookies(resp)

	rawBody, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("读取预上传响应失败: %w", err)
	}

	var envelope struct {
		Status   int               `json:"status"`
		Code     int               `json:"code"`
		Message  string            `json:"message"`
		Data     UploadPreResponse `json:"data"`
		Metadata struct {
			PartSize int64 `json:"part_size"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(rawBody, &envelope); err != nil {
		return nil, fmt.Errorf("解析预上传响应失败: %w", err)
	}
	if resp.StatusCode >= 400 || (envelope.Status != 0 && envelope.Status >= 400) || envelope.Code != 0 {
		return nil, classifyError("预上传", resp.StatusCode, envelope.Status, envelope.Code, envelope.Message)
	}

	out := envelope.Data
	if out.PartSize <= 0 {
		out.PartSize = envelope.Metadata.PartSize
	}
	if out.PartSize <= 0 {
		out.PartSize = DefaultUploadPartSize
	}
	return &out, nil
}

// UploadUpdateHash 向云端校验文件 MD5 与 SHA1，若云端已有相同文件则直接命中秒传
func (c *Client) UploadUpdateHash(ctx context.Context, taskID, md5Hex, sha1Hex string) (bool, string, error) {
	req := map[string]interface{}{
		"task_id": taskID,
		"md5":     strings.ToLower(strings.TrimSpace(md5Hex)),
		"sha1":    strings.ToLower(strings.TrimSpace(sha1Hex)),
	}
	var data struct {
		Finish bool   `json:"finish"`
		FID    string `json:"fid"`
	}
	if err := c.doDriveJSON(ctx, "秒传哈希校验", http.MethodPost, "/1/clouddrive/file/update/hash", nil, req, &data); err != nil {
		return false, "", err
	}
	return data.Finish, data.FID, nil
}

// UploadAuth 请求夸克后端签发单次 OSS 操作（分片 PUT / 合并 POST）所需的 Authorization 签名
func (c *Client) UploadAuth(ctx context.Context, taskID, authInfo, authMeta string) (string, error) {
	req := map[string]interface{}{
		"task_id":   taskID,
		"auth_info": authInfo,
		"auth_meta": authMeta,
	}
	var data struct {
		AuthKey string `json:"auth_key"`
	}
	if err := c.doDriveJSON(ctx, "获取上传授权签名", http.MethodPost, "/1/clouddrive/file/upload/auth", nil, req, &data); err != nil {
		return "", err
	}
	if strings.TrimSpace(data.AuthKey) == "" {
		return "", fmt.Errorf("服务端返回空的 OSS auth_key")
	}
	return data.AuthKey, nil
}

// BuildOSSPartAuthMeta 构造阿里云 OSS 分片 PUT 请求的规范化待签名 auth_meta 字符串
func BuildOSSPartAuthMeta(bucket, objKey, uploadID string, partNumber int, mimeType, gmtDate, ossUA string) string {
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	if ossUA == "" {
		ossUA = ossDefaultUA
	}
	resource := fmt.Sprintf("/%s/%s?partNumber=%d&uploadId=%s",
		bucket, strings.TrimLeft(objKey, "/"), partNumber, uploadID)
	return strings.Join([]string{
		"PUT",
		"",
		mimeType,
		gmtDate,
		"x-oss-date:" + gmtDate,
		"x-oss-user-agent:" + ossUA,
		resource,
	}, "\n")
}

// BuildOSSCompleteAuthMeta 构造阿里云 OSS CompleteMultipartUpload POST 请求的规范化 auth_meta
func BuildOSSCompleteAuthMeta(bucket, objKey, uploadID, contentMD5, callbackBase64, gmtDate, ossUA string) string {
	if ossUA == "" {
		ossUA = ossDefaultUA
	}
	resource := fmt.Sprintf("/%s/%s?uploadId=%s",
		bucket, strings.TrimLeft(objKey, "/"), uploadID)

	lines := []string{
		"POST",
		contentMD5,
		"application/xml",
		gmtDate,
	}
	if callbackBase64 != "" {
		lines = append(lines, "x-oss-callback:"+callbackBase64)
	}
	lines = append(lines,
		"x-oss-date:"+gmtDate,
		"x-oss-user-agent:"+ossUA,
		resource,
	)
	return strings.Join(lines, "\n")
}

// BuildOSSObjectURL 安全拼接 OSS 对象请求地址，避免重复注入 bucket 前缀并兼容本地 httptest URL
func BuildOSSObjectURL(uploadURL, bucket, objKey string, query url.Values) (string, error) {
	raw := strings.TrimSpace(uploadURL)
	if raw == "" {
		return "", fmt.Errorf("upload_url 不能为空")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("解析 upload_url 失败: %w", err)
	}

	host := u.Host
	hostname := u.Hostname()
	// 若为真实 OSS 域名（非环回地址）且 host 尚未携带 bucket 前缀，则自动补全 <bucket>.<host>
	isLoopback := hostname == "127.0.0.1" || hostname == "localhost" || hostname == "::1"
	if !isLoopback && strings.EqualFold(u.Scheme, "http") {
		u.Scheme = "https"
	}
	if bucket != "" && !isLoopback && !strings.HasPrefix(strings.ToLower(hostname), strings.ToLower(bucket)+".") {
		host = bucket + "." + host
	}
	u.Host = host

	basePath := strings.TrimRight(u.Path, "/")
	keyPath := strings.TrimLeft(objKey, "/")
	if keyPath != "" {
		u.Path = basePath + "/" + keyPath
	} else if basePath == "" {
		u.Path = "/"
	}
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	return u.String(), nil
}

// UploadOSSPart 向 OSS 上传单个分片并返回去除双引号后的 ETag
func (c *Client) UploadOSSPart(ctx context.Context, pre *UploadPreResponse, partNumber int, body io.Reader, contentLength int64, mimeType string) (string, error) {
	if pre == nil {
		return "", fmt.Errorf("预上传参数不能为空")
	}
	mimeType = normalizeUploadMIME(mimeType)
	gmtDate := time.Now().UTC().Format(http.TimeFormat)
	authMeta := BuildOSSPartAuthMeta(pre.Bucket, pre.ObjKey, pre.UploadID, partNumber, mimeType, gmtDate, ossDefaultUA)
	authKey, err := c.UploadAuth(ctx, pre.TaskID, pre.AuthInfo, authMeta)
	if err != nil {
		return "", err
	}

	q := url.Values{}
	q.Set("partNumber", strconv.Itoa(partNumber))
	q.Set("uploadId", pre.UploadID)
	targetURL, err := BuildOSSObjectURL(pre.UploadURL, pre.Bucket, pre.ObjKey, q)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, targetURL, body)
	if err != nil {
		return "", fmt.Errorf("创建分片 PUT 请求失败: %w", err)
	}
	if contentLength >= 0 {
		req.ContentLength = contentLength
	}
	req.Header.Set("Authorization", authKey)
	req.Header.Set("Content-Type", mimeType)
	req.Header.Set("Date", gmtDate)
	req.Header.Set("Referer", DefaultReferer)
	req.Header.Set("x-oss-date", gmtDate)
	req.Header.Set("x-oss-user-agent", ossDefaultUA)

	resp, err := c.DownloadHTTPClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("上传分片 #%d 失败: %w", partNumber, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("OSS 上传分片 #%d 返回状态 %d: %s", partNumber, resp.StatusCode, strings.TrimSpace(string(errBody)))
	}

	etag := strings.Trim(strings.TrimSpace(resp.Header.Get("ETag")), "\"")
	return etag, nil
}

// CompleteOSSMultipart 提交所有分片 ETag 与 x-oss-callback 通知 OSS 合并对象
func (c *Client) CompleteOSSMultipart(ctx context.Context, pre *UploadPreResponse, parts []UploadPart) error {
	if pre == nil {
		return fmt.Errorf("预上传参数不能为空")
	}

	sorted := make([]UploadPart, len(parts))
	copy(sorted, parts)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].PartNumber < sorted[j].PartNumber
	})

	// NOTE: 阿里云 OSS 要求 CompleteMultipartUpload 中的 <ETag> 保留字面双引号，避免 xml.Marshal 将其转义为 &#34;
	var xmlBuf bytes.Buffer
	xmlBuf.WriteString("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<CompleteMultipartUpload>\n")
	for _, p := range sorted {
		etag := strings.Trim(strings.TrimSpace(p.ETag), "\"")
		fmt.Fprintf(&xmlBuf, "<Part>\n<PartNumber>%d</PartNumber>\n<ETag>\"", p.PartNumber)
		if err := xml.EscapeText(&xmlBuf, []byte(etag)); err != nil {
			return fmt.Errorf("转义分片 ETag 失败: %w", err)
		}
		xmlBuf.WriteString("\"</ETag>\n</Part>\n")
	}
	xmlBuf.WriteString("</CompleteMultipartUpload>")
	xmlBytes := xmlBuf.Bytes()
	md5Sum := md5.Sum(xmlBytes)
	contentMD5 := base64.StdEncoding.EncodeToString(md5Sum[:])

	var callbackB64 string
	trimmedCB := bytes.TrimSpace(pre.CallbackRaw)
	if len(trimmedCB) > 0 && string(trimmedCB) != "null" && string(trimmedCB) != "\"\"" {
		var cbStr string
		if trimmedCB[0] == '"' {
			_ = json.Unmarshal(trimmedCB, &cbStr)
			callbackB64 = base64.StdEncoding.EncodeToString([]byte(cbStr))
		} else {
			callbackB64 = base64.StdEncoding.EncodeToString(trimmedCB)
		}
	}

	gmtDate := time.Now().UTC().Format(http.TimeFormat)
	authMeta := BuildOSSCompleteAuthMeta(pre.Bucket, pre.ObjKey, pre.UploadID, contentMD5, callbackB64, gmtDate, ossDefaultUA)
	authKey, err := c.UploadAuth(ctx, pre.TaskID, pre.AuthInfo, authMeta)
	if err != nil {
		return err
	}

	q := url.Values{}
	q.Set("uploadId", pre.UploadID)
	targetURL, err := BuildOSSObjectURL(pre.UploadURL, pre.Bucket, pre.ObjKey, q)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(xmlBytes))
	if err != nil {
		return fmt.Errorf("创建分片合并请求失败: %w", err)
	}
	req.ContentLength = int64(len(xmlBytes))
	req.Header.Set("Authorization", authKey)
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-MD5", contentMD5)
	req.Header.Set("Date", gmtDate)
	req.Header.Set("Referer", DefaultReferer)
	req.Header.Set("x-oss-date", gmtDate)
	req.Header.Set("x-oss-user-agent", ossDefaultUA)
	if callbackB64 != "" {
		req.Header.Set("x-oss-callback", callbackB64)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("请求 OSS 合并分片失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("OSS 合并分片返回状态 %d: %s", resp.StatusCode, strings.TrimSpace(string(errBody)))
	}
	return nil
}

// UploadFinish 通知夸克网盘服务端完成上传落盘，并失效相关目录缓存
func (c *Client) UploadFinish(ctx context.Context, objKey, taskID, pdirFID string) (string, error) {
	req := map[string]interface{}{
		"obj_key": objKey,
		"task_id": taskID,
	}
	var data struct {
		FID string `json:"fid"`
	}
	if err := c.doDriveJSON(ctx, "确认完成上传", http.MethodPost, "/1/clouddrive/file/upload/finish", nil, req, &data); err != nil {
		return "", err
	}
	if pdirFID != "" {
		c.resolver.InvalidateDir(pdirFID)
	} else {
		c.resolver.InvalidateAll()
	}
	return data.FID, nil
}
