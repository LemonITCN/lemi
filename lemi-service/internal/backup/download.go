package backup

import (
	"context"
	"io"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/backup/cryptstream"
	"github.com/LemonITCN/lemi/lemi-service/internal/downloads"
)

// plainName 去掉压缩与加密扩展名（x.sql.zst.lemienc → x.sql）。
func plainName(name string) string {
	name = strings.TrimSuffix(name, cryptstream.Ext)
	for _, ext := range []string{".zst", ".gz"} {
		name = strings.TrimSuffix(name, ext)
	}
	return name
}

// DownloadJob 生成一次性下载：format=raw 原样下载（压缩、加密的原文件）；format=sql 服务端解密解压后流式输出
// （SQL 备份得到可直接导入的 .sql，SQLite 文件快照得到 .db）。
func (s *Service) DownloadJob(ctx context.Context, r *Run, userID, format, targetID string) (*downloads.Job, error) {
	if r.Status != StatusSuccess {
		return nil, apperr.Bad("只能下载成功的备份")
	}
	if _, err := s.okFiles(ctx, r, targetID); err != nil {
		return nil, err
	}
	switch format {
	case "", "raw":
		return &downloads.Job{UserID: userID, Filename: r.FileName, ContentType: "application/octet-stream", Size: r.Size,
			Write: func(ctx context.Context, w io.Writer) error {
				rc, _, err := s.openArtifact(ctx, r, targetID)
				if err != nil {
					return err
				}
				defer rc.Close()
				_, err = io.Copy(w, rc)
				return err
			}}, nil
	case "sql":
		if r.Encrypted {
			if _, err := s.runPassphrase(r); err != nil {
				return nil, apperr.Bad("%s", err.Error())
			}
		}
		ct := "application/sql; charset=utf-8"
		if r.Method == MethodSQLiteFile {
			ct = "application/vnd.sqlite3"
		}
		return &downloads.Job{UserID: userID, Filename: plainName(r.FileName), ContentType: ct, Size: r.RawSize,
			Write: func(ctx context.Context, w io.Writer) error {
				rc, _, err := s.openDecoded(ctx, r, targetID, nil)
				if err != nil {
					return err
				}
				defer rc.Close()
				_, err = io.Copy(w, rc)
				return err
			}}, nil
	}
	return nil, apperr.Bad("下载格式只能是 raw 或 sql")
}
