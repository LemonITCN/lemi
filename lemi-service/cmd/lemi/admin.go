package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/auth"
	"github.com/LemonITCN/lemi/lemi-service/internal/config"
	"github.com/LemonITCN/lemi/lemi-service/internal/crypt"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/settings"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
)

func init() {
	commands["admin"] = command{Usage: "admin <reset-password|reset-totp|unlock|list-users> [用户名|IP] [--data 目录]   本机维护", Run: runAdmin}
}

func runAdmin(args []string) error {
	if len(args) == 0 {
		return errors.New("用法：lemi admin reset-password <用户名> | reset-totp <用户名> | unlock <用户名或 IP> | list-users")
	}
	sub, rest := args[0], args[1:]
	var target string
	if len(rest) > 0 && rest[0] != "" && rest[0][0] != '-' {
		target, rest = rest[0], rest[1:]
	}
	_, data, _, err := commonFlags("admin", rest)
	if err != nil {
		return err
	}
	cfg, err := config.Load(*data, "")
	if err != nil {
		return err
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Path("lemi.db"))
	if err != nil {
		return err
	}
	defer st.Close()
	key, _, err := crypt.LoadOrCreateKey(cfg.MasterKey, cfg.Path("master.key"))
	if err != nil {
		return err
	}
	box, err := crypt.New(key)
	if err != nil {
		return err
	}
	acc := accounts.New(st)
	au := auth.New(st, acc, settings.New(st), box)
	audit := func(action, target string) {
		_, _ = st.Exec(ctx, `INSERT INTO audit_log (at, source, action, target) VALUES (?, 'cli', ?, ?)`, store.Now(), action, target)
	}
	switch sub {
	case "list-users":
		list, err := acc.Users(ctx)
		if err != nil {
			return err
		}
		for _, u := range list {
			fmt.Printf("%-24s %-6s %-8s 两步验证:%v\n", u.Username, u.Role, u.Status, u.TOTPEnabled)
		}
		return nil
	case "reset-password":
		u, err := acc.UserByName(ctx, target)
		if err != nil {
			return fmt.Errorf("用户 %q 不存在", target)
		}
		pw := ids.RandomToken(8) + "-" + ids.RandomToken(8)
		h, err := auth.HashPassword(pw)
		if err != nil {
			return err
		}
		u.PasswordHash, u.MustChangePassword, u.PasswordChangedAt = h, true, store.Now()
		if err := acc.SaveUser(ctx, u); err != nil {
			return err
		}
		au.RevokeOthers(ctx, u.ID, "")
		_ = au.Unlock(ctx, u.Username)
		audit("user.reset_password", u.Username)
		fmt.Printf("已重置 %s 的口令，临时口令：%s\n登录后必须修改。\n", u.Username, pw)
		return nil
	case "reset-totp":
		u, err := acc.UserByName(ctx, target)
		if err != nil {
			return fmt.Errorf("用户 %q 不存在", target)
		}
		if err := au.ResetTOTP(ctx, u.ID); err != nil {
			return err
		}
		au.RevokeOthers(ctx, u.ID, "")
		audit("user.reset_totp", u.Username)
		fmt.Printf("已清除 %s 的两步验证，下次登录需要重新绑定。\n", u.Username)
		return nil
	case "unlock":
		if target == "" {
			return errors.New("请指定用户名或 IP")
		}
		if err := au.Unlock(ctx, target); err != nil {
			return err
		}
		audit("auth.unlock", target)
		fmt.Println("已解除锁定。")
		return nil
	}
	return fmt.Errorf("未知的子命令 %q", sub)
}
