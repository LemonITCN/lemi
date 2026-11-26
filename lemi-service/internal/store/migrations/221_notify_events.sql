-- 通知渠道的事件订阅：JSON 数组，空 = 收全部事件（由备份计划、健康检查等选中该渠道时）
ALTER TABLE notify_channels ADD COLUMN events TEXT NOT NULL DEFAULT '[]';
