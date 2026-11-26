package tools

import (
	"encoding/json"
)

// 备份与健康检查工具：备份模块实现 BackupInfo 后才出现在工具列表里。

func init() {
	r := Default
	avail := func(env *Env) bool { return env.Svc != nil && env.Svc.Backup != nil }
	r.Register(&Tool{
		Name: "list_backups", Title: "备份记录", Kind: KindRead, Available: avail,
		Description: "列出备份记录（时间、方式、大小、状态、校验结果）。不指定 connection_id 时列出全部可见连接的最近备份。",
		Schema: obj(map[string]any{
			"connection_id": str("可选：只看这个连接"),
			"limit":         integer("最多几条（默认 20）"),
		}),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				ConnectionID string `json:"connection_id"`
				Limit        int    `json:"limit"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			ids, err := env.backupTargets(in.ConnectionID)
			if err != nil {
				return nil, err
			}
			if in.Limit <= 0 || in.Limit > 200 {
				in.Limit = 20
			}
			v, err := env.Svc.Backup.AIListBackups(env.Ctx, env.User, env.WorkspaceID, ids, in.Limit)
			if err != nil {
				return nil, fail("读取备份记录失败：%s", errText(err))
			}
			return jsonResult(v), nil
		},
	})
	r.Register(&Tool{
		Name: "backup_status", Title: "备份计划", Kind: KindRead, Available: avail,
		Description: "查看备份计划与状态：每个计划的范围、方式、频率、下次运行时间、最近一次结果。",
		Schema:      obj(map[string]any{"connection_id": str("可选：只看这个连接")}),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				ConnectionID string `json:"connection_id"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			ids, err := env.backupTargets(in.ConnectionID)
			if err != nil {
				return nil, err
			}
			v, err := env.Svc.Backup.AIBackupStatus(env.Ctx, env.User, env.WorkspaceID, ids)
			if err != nil {
				return nil, fail("读取备份计划失败：%s", errText(err))
			}
			return jsonResult(v), nil
		},
	})
	r.Register(&Tool{
		Name: "health_status", Title: "健康状态", Kind: KindRead, Available: avail,
		Description: "查看连接的健康检查状态：在线 / 下线、最近延迟、可用率、最近的错误。",
		Schema:      obj(map[string]any{"connection_id": str("可选：只看这个连接")}),
		Handler: func(env *Env, args json.RawMessage) (*Result, error) {
			var in struct {
				ConnectionID string `json:"connection_id"`
			}
			if err := bind(args, &in); err != nil {
				return nil, err
			}
			ids, err := env.backupTargets(in.ConnectionID)
			if err != nil {
				return nil, err
			}
			v, err := env.Svc.Backup.AIHealthStatus(env.Ctx, env.User, env.WorkspaceID, ids)
			if err != nil {
				return nil, fail("读取健康状态失败：%s", errText(err))
			}
			return jsonResult(v), nil
		},
	})
}

func (env *Env) backupTargets(connID string) ([]string, error) {
	if connID != "" {
		a, err := env.Access(connID)
		if err != nil {
			return nil, err
		}
		return []string{a.Conn.ID}, nil
	}
	ids := env.visibleIDs()
	if len(ids) == 0 {
		return nil, fail("没有可见的连接")
	}
	return ids, nil
}

func jsonResult(v any) *Result {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return &Result{Text: "（结果无法序列化）", IsError: true}
	}
	return &Result{Text: string(b), Data: v}
}
