package quarkpan

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
)

const defaultListPageSize = 100

// ListDirByFID 根据目录 FID 自动翻页拉取该目录下的全部文件与子目录
func (c *Client) ListDirByFID(ctx context.Context, pdirFID string, opt *ListOptions) ([]FileItem, error) {
	if strings.TrimSpace(pdirFID) == "" {
		pdirFID = RootFID
	}
	if opt != nil && !opt.ForceRefresh && c.resolver != nil && opt.SortField == "" && opt.SortOrder == "" {
		if cached, ok := c.resolver.getCachedChildren(pdirFID); ok {
			return cached, nil
		}
	}

	sortExpr := buildSortExpr(opt)
	var all []FileItem
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("pdir_fid", pdirFID)
		q.Set("_page", strconv.Itoa(page))
		q.Set("_size", strconv.Itoa(defaultListPageSize))
		q.Set("_fetch_total", "1")
		q.Set("_fetch_sub_dirs", "0")
		q.Set("_sort", sortExpr)

		var data struct {
			List []FileItem `json:"list"`
		}
		if err := c.doDriveJSON(ctx, "列举目录", http.MethodGet, "/1/clouddrive/file/sort", q, nil, &data); err != nil {
			return nil, err
		}

		all = append(all, data.List...)
		if len(data.List) < defaultListPageSize {
			break
		}
	}

	if c.resolver != nil {
		c.resolver.cacheDirItems(pdirFID, "", all)
	}
	return all, nil
}

func buildSortExpr(opt *ListOptions) string {
	field := "file_name"
	order := "asc"
	if opt != nil {
		switch strings.ToLower(strings.TrimSpace(opt.SortField)) {
		case "size":
			field = "size"
		case "time", "updated_at", "mtime":
			field = "updated_at"
		case "name", "file_name":
			field = "file_name"
		}
		switch strings.ToLower(strings.TrimSpace(opt.SortOrder)) {
		case "desc":
			order = "desc"
		case "asc":
			order = "asc"
		}
	}
	return "file_type:asc," + field + ":" + order
}

// ListDir 列举指定网盘路径下的所有文件和子目录，并自动回填 FullPath
func (c *Client) ListDir(ctx context.Context, absPath string, opt *ListOptions) ([]FileItem, error) {
	dirFID, normPath, err := c.resolver.ResolveDirFID(ctx, absPath)
	if err != nil {
		return nil, err
	}
	items, err := c.ListDirByFID(ctx, dirFID, opt)
	if err != nil {
		return nil, err
	}
	for i := range items {
		if strings.HasPrefix(normPath, FIDPrefix) {
			items[i].FullPath = normPath + "/" + items[i].FileName
		} else {
			items[i].FullPath = path.Join(normPath, items[i].FileName)
		}
	}
	return items, nil
}

// SearchResult 描述网盘搜索返回结果及总数
type SearchResult struct {
	Items []FileItem `json:"items"`
	Total int        `json:"total"`
}

// Search 按关键字全盘搜索文件与目录
func (c *Client) Search(ctx context.Context, keyword string, page, size int) (*SearchResult, error) {
	kw := strings.TrimSpace(keyword)
	if kw == "" {
		return nil, fmt.Errorf("搜索关键字不能为空")
	}
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 50
	}

	q := url.Values{}
	q.Set("q", kw)
	q.Set("_page", strconv.Itoa(page))
	q.Set("_size", strconv.Itoa(size))
	q.Set("_fetch_total", "1")
	q.Set("_is_hl", "0")
	q.Set("_sort", "file_type:desc,updated_at:desc")

	var data struct {
		List  []FileItem `json:"list"`
		Total int        `json:"total"`
	}
	if err := c.doDriveJSON(ctx, "搜索网盘文件", http.MethodGet, "/1/clouddrive/file/search", q, nil, &data); err != nil {
		return nil, err
	}
	return &SearchResult{
		Items: data.List,
		Total: data.Total,
	}, nil
}

// Mkdir 在指定父目录 FID 下创建单个子目录，返回新目录的 FID
func (c *Client) Mkdir(ctx context.Context, parentFID, name string) (string, error) {
	if strings.TrimSpace(parentFID) == "" {
		parentFID = RootFID
	}
	dirName := strings.TrimSpace(name)
	if dirName == "" || strings.Contains(dirName, "/") {
		return "", fmt.Errorf("无效的目录名称: %q", name)
	}

	req := map[string]interface{}{
		"pdir_fid":      parentFID,
		"file_name":     dirName,
		"dir_path":      "",
		"dir_init_lock": false,
	}
	var data struct {
		FID string `json:"fid"`
	}
	if err := c.doDriveJSON(ctx, "创建目录", http.MethodPost, "/1/clouddrive/file", nil, req, &data); err != nil {
		return "", err
	}
	c.resolver.InvalidateDir(parentFID)
	return data.FID, nil
}

// MkdirAll 按绝对路径递归创建不存在的各级目录，并返回最终目录 FID
func (c *Client) MkdirAll(ctx context.Context, absPath string) (string, error) {
	if fid, ok := ParseFIDPrefix(absPath); ok {
		return fid, nil
	}
	clean := NormalizePath("/", absPath)
	if clean == "/" {
		return RootFID, nil
	}

	segments := strings.Split(strings.TrimPrefix(clean, "/"), "/")
	curFID := RootFID
	for _, seg := range segments {
		if seg == "" {
			continue
		}
		items, err := c.ListDirByFID(ctx, curFID, &ListOptions{ForceRefresh: true})
		if err != nil {
			return "", err
		}
		foundFID := ""
		for _, it := range items {
			if it.FileName == seg {
				if !it.IsDir {
					return "", fmt.Errorf("同名文件已存在且不是目录: %s", seg)
				}
				foundFID = it.FID
				break
			}
		}
		if foundFID == "" {
			foundFID, err = c.Mkdir(ctx, curFID, seg)
			if err != nil {
				return "", err
			}
		}
		curFID = foundFID
	}
	return curFID, nil
}

// Rename 重命名指定 FID 的文件或目录
func (c *Client) Rename(ctx context.Context, fid, newName string) error {
	if strings.TrimSpace(fid) == "" || fid == RootFID {
		return fmt.Errorf("不允许重命名根目录或空 FID")
	}
	name := strings.TrimSpace(newName)
	if name == "" || strings.Contains(name, "/") {
		return fmt.Errorf("无效的新名称: %q", newName)
	}
	req := map[string]interface{}{
		"fid":       fid,
		"file_name": name,
	}
	if err := c.doDriveJSON(ctx, "重命名文件", http.MethodPost, "/1/clouddrive/file/rename", nil, req, nil); err != nil {
		return err
	}
	c.resolver.InvalidateAll()
	return nil
}

type asyncTaskResponse struct {
	TaskID string `json:"task_id"`
	Finish bool   `json:"finish"`
}

// Move 将一组 FID 移动至目标目录并等待云端异步任务完成
func (c *Client) Move(ctx context.Context, fids []string, toPDirFID string) error {
	if len(fids) == 0 {
		return fmt.Errorf("未指定待移动的文件 FID")
	}
	if strings.TrimSpace(toPDirFID) == "" {
		toPDirFID = RootFID
	}
	req := map[string]interface{}{
		"action_type":  1,
		"to_pdir_fid":  toPDirFID,
		"filelist":     fids,
		"exclude_fids": []string{},
	}
	var resp asyncTaskResponse
	if err := c.doDriveJSON(ctx, "移动文件", http.MethodPost, "/1/clouddrive/file/move", nil, req, &resp); err != nil {
		return err
	}
	if !resp.Finish && resp.TaskID != "" {
		if _, err := c.WaitTask(ctx, resp.TaskID); err != nil {
			return err
		}
	}
	c.resolver.InvalidateAll()
	return nil
}

// Copy 将一组 FID 复制至目标目录并等待云端异步任务完成
func (c *Client) Copy(ctx context.Context, fids []string, toPDirFID string) error {
	if len(fids) == 0 {
		return fmt.Errorf("未指定待复制的文件 FID")
	}
	if strings.TrimSpace(toPDirFID) == "" {
		toPDirFID = RootFID
	}
	req := map[string]interface{}{
		"action_type":  1,
		"to_pdir_fid":  toPDirFID,
		"filelist":     fids,
		"exclude_fids": []string{},
	}
	var resp asyncTaskResponse
	if err := c.doDriveJSON(ctx, "复制文件", http.MethodPost, "/1/clouddrive/file/copy", nil, req, &resp); err != nil {
		return err
	}
	if !resp.Finish && resp.TaskID != "" {
		if _, err := c.WaitTask(ctx, resp.TaskID); err != nil {
			return err
		}
	}
	c.resolver.InvalidateAll()
	return nil
}

// Delete 将一组 FID 移入回收站并等待异步任务完成
func (c *Client) Delete(ctx context.Context, fids []string) error {
	if len(fids) == 0 {
		return fmt.Errorf("未指定待删除的文件 FID")
	}
	req := map[string]interface{}{
		"action_type":  2,
		"filelist":     fids,
		"exclude_fids": []string{},
	}
	var resp asyncTaskResponse
	if err := c.doDriveJSON(ctx, "删除文件", http.MethodPost, "/1/clouddrive/file/delete", nil, req, &resp); err != nil {
		return err
	}
	if !resp.Finish && resp.TaskID != "" {
		if _, err := c.WaitTask(ctx, resp.TaskID); err != nil {
			return err
		}
	}
	c.resolver.InvalidateAll()
	return nil
}
