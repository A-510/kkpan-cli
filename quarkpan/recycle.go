package quarkpan

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// ListRecycle 分页查询夸克网盘回收站中的文件与目录记录
func (c *Client) ListRecycle(ctx context.Context, page, size int) (*RecycleListResult, error) {
	if page <= 0 {
		page = 1
	}
	if size <= 0 || size > 100 {
		size = 50
	}

	q := url.Values{}
	q.Set("_page", strconv.Itoa(page))
	q.Set("_size", strconv.Itoa(size))
	q.Set("_fetch_total", "1")

	var data struct {
		List []RecycleItem `json:"list"`
	}
	var meta apiMetadata
	if err := c.doDriveJSONWithMeta(ctx, "查询回收站列表", http.MethodGet, "/1/clouddrive/file/recycle/list", q, nil, &data, &meta); err != nil {
		return nil, err
	}

	items := data.List
	if items == nil {
		items = []RecycleItem{}
	}
	for i := range items {
		if strings.TrimSpace(items[i].RecordID) == "" && strings.TrimSpace(items[i].FID) != "" {
			items[i].RecordID = items[i].FID
		}
	}
	total := meta.Total
	if total <= 0 {
		total = len(items)
	}
	return &RecycleListResult{
		Items: items,
		Total: total,
		Page:  page,
		Size:  size,
	}, nil
}

// RestoreRecycle 根据 record_id 列表从回收站还原文件或目录，并刷新路径解析缓存
func (c *Client) RestoreRecycle(ctx context.Context, recordIDs []string) error {
	cleanIDs := cleanNonEmptyIDs(recordIDs)
	if len(cleanIDs) == 0 {
		return fmt.Errorf("请至少指定一个待还原的回收站记录 ID (record_id)")
	}

	req := map[string]interface{}{
		"select_mode": 2,
		"record_list": cleanIDs,
	}
	var resp asyncTaskResponse
	if err := c.doDriveJSON(ctx, "还原回收站文件", http.MethodPost, "/1/clouddrive/file/recycle/restore", nil, req, &resp); err != nil {
		return err
	}
	if !resp.Finish && strings.TrimSpace(resp.TaskID) != "" {
		if _, err := c.WaitTask(ctx, resp.TaskID); err != nil {
			return err
		}
	}
	if c.resolver != nil {
		c.resolver.InvalidateAll()
	}
	return nil
}

// DeleteRecycle 根据 record_id 列表从回收站彻底删除文件或目录（不可恢复）
func (c *Client) DeleteRecycle(ctx context.Context, recordIDs []string) error {
	cleanIDs := cleanNonEmptyIDs(recordIDs)
	if len(cleanIDs) == 0 {
		return fmt.Errorf("请至少指定一个待彻底删除的回收站记录 ID (record_id)")
	}

	req := map[string]interface{}{
		"select_mode": 2,
		"record_list": cleanIDs,
	}
	var resp asyncTaskResponse
	if err := c.doDriveJSON(ctx, "彻底删除回收站文件", http.MethodPost, "/1/clouddrive/file/recycle/remove", nil, req, &resp); err != nil {
		return err
	}
	if !resp.Finish && strings.TrimSpace(resp.TaskID) != "" {
		if _, err := c.WaitTask(ctx, resp.TaskID); err != nil {
			return err
		}
	}
	return nil
}

// ClearRecycle 清空夸克网盘回收站内的全部记录（不可恢复）
func (c *Client) ClearRecycle(ctx context.Context) error {
	req := map[string]interface{}{
		"select_mode": 1,
		"record_list": []string{},
	}
	var resp asyncTaskResponse
	if err := c.doDriveJSON(ctx, "清空回收站", http.MethodPost, "/1/clouddrive/file/recycle/remove", nil, req, &resp); err != nil {
		return err
	}
	if !resp.Finish && strings.TrimSpace(resp.TaskID) != "" {
		if _, err := c.WaitTask(ctx, resp.TaskID); err != nil {
			return err
		}
	}
	return nil
}

func cleanNonEmptyIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, raw := range ids {
		if id := strings.TrimSpace(raw); id != "" {
			out = append(out, id)
		}
	}
	return out
}
