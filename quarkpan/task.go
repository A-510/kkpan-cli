package quarkpan

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// WaitTask 轮询夸克网盘异步任务接口直到完成、失败或上下文取消
func (c *Client) WaitTask(ctx context.Context, taskID string) (*TaskStatus, error) {
	id := strings.TrimSpace(taskID)
	if id == "" {
		return nil, nil
	}

	interval := 300 * time.Millisecond
	const maxInterval = 1200 * time.Millisecond

	for retry := 0; ; retry++ {
		q := url.Values{}
		q.Set("task_id", id)
		q.Set("retry_index", strconv.Itoa(retry))

		var st TaskStatus
		if err := c.doDriveJSON(ctx, "查询异步任务", http.MethodGet, "/1/clouddrive/task", q, nil, &st); err != nil {
			return nil, err
		}
		if st.TaskID == "" {
			st.TaskID = id
		}

		switch st.Status {
		case 2:
			return &st, nil
		case 3:
			msg := strings.TrimSpace(st.Message)
			if msg == "" {
				msg = "任务执行异常"
			}
			return &st, fmt.Errorf("%w (%s): %s", ErrTaskFailed, id, msg)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, fmt.Errorf("等待云端任务 %s 中断: %w", id, ctx.Err())
		case <-timer.C:
		}

		if interval < maxInterval {
			interval += 200 * time.Millisecond
			if interval > maxInterval {
				interval = maxInterval
			}
		}
	}
}
