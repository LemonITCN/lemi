package notify

import (
	"context"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/tasks"
)

// NotifyTask 定时智能体任务结束后发通知（实现 tasks.TaskNotifier）：异步发到任务选的渠道，失败重试并记录。
func (s *Service) NotifyTask(ctx context.Context, n tasks.TaskNotification) error {
	if len(n.ChannelIDs) == 0 {
		return nil
	}
	parts := []string{}
	if m := strings.TrimSpace(n.Message); m != "" {
		parts = append(parts, m)
	}
	if sm := strings.TrimSpace(n.Summary); sm != "" && sm != strings.TrimSpace(n.Message) {
		parts = append(parts, sm)
	}
	if e := strings.TrimSpace(n.Error); e != "" {
		parts = append(parts, "错误："+e)
	}
	level := n.Level
	if level == "" {
		level = "info"
		if n.Status == "error" {
			level = "error"
		}
	}
	title := n.Title
	if title == "" {
		title = "定时任务：" + n.TaskName
	}
	s.Send(ctx, n.WorkspaceID, n.ChannelIDs, Event{Kind: EvTaskResult, Level: level, Title: title, Message: strings.Join(parts, "\n\n"), Link: n.Link})
	return nil
}
