package quarkpan

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	reQuarkShareURL  = regexp.MustCompile(`(?i)https?://(?:pan|drive)\.quark\.cn/s/([A-Za-z0-9_-]+)(?:[^\s]*)?`)
	reShareShortID   = regexp.MustCompile(`^[A-Za-z0-9]{8,32}$`)
	reSharePassLabel = regexp.MustCompile(`(?i)(?:提取码|密码|访问码|口令)\s*[:：]?\s*([A-Za-z0-9]{4})\b`)
	reSharePassPlain = regexp.MustCompile(`^[A-Za-z0-9]{4}$`)
)

// ParseShareInput 从分享链接、整段夸克分享文本或独立参数中提取 pwdID 与可选提取码
func ParseShareInput(args []string) (pwdID, passcode string, err error) {
	if len(args) == 0 {
		return "", "", fmt.Errorf("请提供夸克网盘分享链接或分享文本")
	}

	var rawParts []string
	for _, a := range args {
		if t := strings.TrimSpace(a); t != "" {
			rawParts = append(rawParts, t)
		}
	}
	if len(rawParts) == 0 {
		return "", "", fmt.Errorf("请提供夸克网盘分享链接或分享文本")
	}

	joined := strings.Join(rawParts, " ")

	// 1. 优先识别完整夸克分享 URL，同时解析查询参数中的 pwd
	if m := reQuarkShareURL.FindStringSubmatch(joined); len(m) >= 2 {
		pwdID = m[1]
		if parsed, pErr := url.Parse(m[0]); pErr == nil {
			if qp := strings.TrimSpace(parsed.Query().Get("pwd")); qp != "" {
				passcode = qp
			}
		}
	}

	// 2. 从中文标签（“提取码：xxxx”）抽取提取码
	if m := reSharePassLabel.FindStringSubmatch(joined); len(m) >= 2 {
		passcode = m[1]
	}

	// 3. 兼容直接传入裸 pwd_id（如 "abc123def456"）
	if pwdID == "" {
		for _, tok := range rawParts {
			for _, word := range strings.Fields(tok) {
				clean := strings.Trim(word, `"'(),;。，！!`)
				if reShareShortID.MatchString(clean) {
					pwdID = clean
					break
				}
			}
			if pwdID != "" {
				break
			}
		}
	}

	if pwdID == "" {
		return "", "", fmt.Errorf("未识别到有效的夸克分享链接或 pwd_id")
	}

	// 4. 若额外传入独立 4 位提取码参数（例如 `transfer <url> 1a2b`），以显式参数为准覆盖前面的匹配
	if len(rawParts) >= 2 {
		last := strings.TrimSpace(rawParts[len(rawParts)-1])
		if reSharePassPlain.MatchString(last) && !strings.EqualFold(last, pwdID) {
			passcode = last
		}
	}

	return pwdID, strings.TrimSpace(passcode), nil
}

// GetShareToken 校验分享链接与提取码，获取后续访问与转存所需的 stoken
func (c *Client) GetShareToken(ctx context.Context, pwdID, passcode string) (*ShareTokenInfo, error) {
	id := strings.TrimSpace(pwdID)
	if id == "" {
		return nil, fmt.Errorf("分享 pwd_id 不能为空")
	}
	req := map[string]interface{}{
		"pwd_id":   id,
		"passcode": strings.TrimSpace(passcode),
	}
	var info ShareTokenInfo
	if err := c.doDriveJSON(ctx, "校验分享链接", http.MethodPost, "/1/clouddrive/share/sharepage/token", nil, req, &info); err != nil {
		return nil, err
	}
	if info.PWDID == "" {
		info.PWDID = id
	}
	if strings.TrimSpace(info.SToken) == "" {
		return nil, fmt.Errorf("校验分享链接失败: 服务端未返回 stoken")
	}
	return &info, nil
}

// ListShareFiles 自动翻页列举分享链接内指定目录下的文件与目录列表
func (c *Client) ListShareFiles(ctx context.Context, pwdID, stoken, pdirFID string) (*ShareDetailResult, error) {
	id := strings.TrimSpace(pwdID)
	tok := strings.TrimSpace(stoken)
	if id == "" || tok == "" {
		return nil, fmt.Errorf("分享 pwd_id 与 stoken 不能为空")
	}
	dirFID := strings.TrimSpace(pdirFID)
	if dirFID == "" {
		dirFID = RootFID
	}

	res := &ShareDetailResult{
		Items: []ShareFileItem{},
	}

	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("pwd_id", id)
		q.Set("stoken", tok)
		q.Set("pdir_fid", dirFID)
		q.Set("force", "0")
		q.Set("_page", strconv.Itoa(page))
		q.Set("_size", strconv.Itoa(defaultListPageSize))
		q.Set("_fetch_banner", "0")
		q.Set("_fetch_share", "1")
		q.Set("_fetch_total", "1")
		q.Set("_sort", "file_type:asc,file_name:asc")

		var data struct {
			Share *ShareInfo      `json:"share"`
			List  []ShareFileItem `json:"list"`
		}
		var meta apiMetadata
		if err := c.doDriveJSONWithMeta(ctx, "读取分享文件列表", http.MethodGet, "/1/clouddrive/share/sharepage/detail", q, nil, &data, &meta); err != nil {
			return nil, err
		}

		if res.Share == nil && data.Share != nil {
			res.Share = data.Share
		}
		res.Items = append(res.Items, data.List...)
		if meta.Total > 0 {
			res.Total = meta.Total
		}
		if len(data.List) < defaultListPageSize || (res.Total > 0 && len(res.Items) >= res.Total) {
			break
		}
	}
	if res.Total == 0 {
		res.Total = len(res.Items)
	}
	return res, nil
}

// SaveShareFiles 将分享中的文件或目录转存到个人网盘指定目录，并等待异步任务完成
func (c *Client) SaveShareFiles(ctx context.Context, req ShareSaveRequest) (*ShareSaveResult, error) {
	pwdID := strings.TrimSpace(req.PWDID)
	stoken := strings.TrimSpace(req.SToken)
	if pwdID == "" || stoken == "" {
		return nil, fmt.Errorf("转存失败: pwd_id 与 stoken 不能为空")
	}
	if len(req.FIDs) == 0 || len(req.FIDs) != len(req.FIDTokens) {
		return nil, fmt.Errorf("转存失败: 待转存文件列表与 token 数量不匹配")
	}
	pdirFID := strings.TrimSpace(req.PDirFID)
	if pdirFID == "" {
		pdirFID = RootFID
	}
	toPDirFID := strings.TrimSpace(req.ToPDirFID)
	if toPDirFID == "" {
		toPDirFID = RootFID
	}

	body := map[string]interface{}{
		"fid_list":       req.FIDs,
		"fid_token_list": req.FIDTokens,
		"to_pdir_fid":    toPDirFID,
		"pwd_id":         pwdID,
		"stoken":         stoken,
		"pdir_fid":       pdirFID,
		"scene":          "link",
	}

	var resp struct {
		TaskID        string      `json:"task_id"`
		Finish        bool        `json:"finish"`
		SaveAs        *TaskSaveAs `json:"save_as"`
		SaveAsTopFIDs []string    `json:"save_as_top_fids"`
	}
	if err := c.doDriveJSON(ctx, "转存分享文件", http.MethodPost, "/1/clouddrive/share/sharepage/save", nil, body, &resp); err != nil {
		return nil, err
	}

	out := &ShareSaveResult{
		TaskID:    resp.TaskID,
		ToPDirFID: toPDirFID,
	}
	if resp.SaveAs != nil && len(resp.SaveAs.SaveAsTopFIDs) > 0 {
		out.SavedTopFIDs = resp.SaveAs.SaveAsTopFIDs
	} else if len(resp.SaveAsTopFIDs) > 0 {
		out.SavedTopFIDs = resp.SaveAsTopFIDs
	}

	if !resp.Finish && strings.TrimSpace(resp.TaskID) != "" {
		st, err := c.WaitTask(ctx, resp.TaskID)
		if err != nil {
			return nil, err
		}
		if fids := st.TopFIDs(); len(fids) > 0 {
			out.SavedTopFIDs = fids
		}
	}

	if c.resolver != nil {
		c.resolver.InvalidateDir(toPDirFID)
	}
	return out, nil
}

func mapShareExpiredType(periodDays int) (int, error) {
	switch periodDays {
	case 0:
		return 1, nil
	case 1:
		return 2, nil
	case 7:
		return 3, nil
	case 30:
		return 4, nil
	default:
		return 0, fmt.Errorf("不支持的分享有效天数 %d（夸克网盘仅支持 0=永久 / 1 / 7 / 30 天）", periodDays)
	}
}

// CreateShare 创建夸克网盘分享链接并换取最终短链与提取码详情
func (c *Client) CreateShare(ctx context.Context, opts CreateShareOptions) (*CreateShareResult, error) {
	if len(opts.FIDs) == 0 {
		return nil, fmt.Errorf("请至少选择一个待分享的文件或目录")
	}
	expiredType, err := mapShareExpiredType(opts.PeriodDays)
	if err != nil {
		return nil, err
	}

	passcode := strings.TrimSpace(opts.Passcode)
	urlType := 1
	if passcode != "" {
		if !reSharePassPlain.MatchString(passcode) {
			return nil, fmt.Errorf("提取码必须为 4 位英文字母或数字")
		}
		urlType = 2
	}

	reqBody := map[string]interface{}{
		"fid_list":     opts.FIDs,
		"title":        strings.TrimSpace(opts.Title),
		"url_type":     urlType,
		"expired_type": expiredType,
	}
	if urlType == 2 {
		reqBody["passcode"] = passcode
	}

	var createResp struct {
		TaskID  string `json:"task_id"`
		ShareID string `json:"share_id"`
		Finish  bool   `json:"finish"`
	}
	if err := c.doDriveJSON(ctx, "创建网盘分享", http.MethodPost, "/1/clouddrive/share", nil, reqBody, &createResp); err != nil {
		return nil, err
	}

	shareID := strings.TrimSpace(createResp.ShareID)
	if shareID == "" && strings.TrimSpace(createResp.TaskID) != "" {
		st, err := c.WaitTask(ctx, createResp.TaskID)
		if err != nil {
			return nil, err
		}
		if st != nil {
			shareID = strings.TrimSpace(st.ShareID)
		}
	}
	if shareID == "" {
		return nil, fmt.Errorf("创建网盘分享失败: 服务端未返回 share_id")
	}

	var pwdResp struct {
		ShareID     string `json:"share_id"`
		PWDID       string `json:"pwd_id"`
		Title       string `json:"title"`
		ShareURL    string `json:"share_url"`
		Passcode    string `json:"passcode"`
		URLType     int    `json:"url_type"`
		ExpiredType int    `json:"expired_type"`
		ExpiredAt   int64  `json:"expired_at"`
	}
	if err := c.doDriveJSON(ctx, "获取分享链接详情", http.MethodPost, "/1/clouddrive/share/password", nil, map[string]interface{}{
		"share_id": shareID,
	}, &pwdResp); err != nil {
		return nil, err
	}

	if pwdResp.ShareID == "" {
		pwdResp.ShareID = shareID
	}
	if pwdResp.Passcode == "" && urlType == 2 {
		pwdResp.Passcode = passcode
	}
	if pwdResp.URLType == 0 {
		pwdResp.URLType = urlType
	}
	if pwdResp.ExpiredType == 0 {
		pwdResp.ExpiredType = expiredType
	}

	return &CreateShareResult{
		ShareID:     pwdResp.ShareID,
		PWDID:       pwdResp.PWDID,
		Title:       pwdResp.Title,
		ShareURL:    pwdResp.ShareURL,
		Passcode:    pwdResp.Passcode,
		URLType:     pwdResp.URLType,
		ExpiredType: pwdResp.ExpiredType,
		ExpiredAt:   pwdResp.ExpiredAt,
	}, nil
}

// ListMyShares 分页查询当前账号创建的分享列表
func (c *Client) ListMyShares(ctx context.Context, page, size int) (*MyShareListResult, error) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 50
	}

	q := url.Values{}
	q.Set("_page", strconv.Itoa(page))
	q.Set("_size", strconv.Itoa(size))
	q.Set("_order_field", "created_at")
	q.Set("_order_type", "desc")
	q.Set("_fetch_total", "1")
	q.Set("_fetch_notify_follow", "1")

	var data struct {
		List []MyShareItem `json:"list"`
	}
	var meta apiMetadata
	if err := c.doDriveJSONWithMeta(ctx, "查询我的分享列表", http.MethodGet, "/1/clouddrive/share/mypage/detail", q, nil, &data, &meta); err != nil {
		return nil, err
	}

	items := data.List
	if items == nil {
		items = []MyShareItem{}
	}
	total := meta.Total
	if total <= 0 {
		total = len(items)
	}
	return &MyShareListResult{
		Items: items,
		Total: total,
		Page:  page,
		Size:  size,
	}, nil
}

// CancelShares 批量取消指定 share_id 的分享链接
func (c *Client) CancelShares(ctx context.Context, shareIDs []string) error {
	cleanIDs := make([]string, 0, len(shareIDs))
	for _, raw := range shareIDs {
		if id := strings.TrimSpace(raw); id != "" {
			cleanIDs = append(cleanIDs, id)
		}
	}
	if len(cleanIDs) == 0 {
		return fmt.Errorf("请至少指定一个待取消的 share_id")
	}

	req := map[string]interface{}{
		"share_ids": cleanIDs,
	}
	return c.doDriveJSON(ctx, "取消分享", http.MethodPost, "/1/clouddrive/share/delete", nil, req, nil)
}
