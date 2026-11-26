package conns

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/accounts"
	"github.com/LemonITCN/lemi/lemi-service/internal/apperr"
	"github.com/LemonITCN/lemi/lemi-service/internal/audit"
	"github.com/LemonITCN/lemi/lemi-service/internal/ids"
	"github.com/LemonITCN/lemi/lemi-service/internal/store"
	"github.com/LemonITCN/lemi/lemi-service/internal/web"
	"github.com/go-chi/chi/v5"
)

// SQLiteFile 上传的 SQLite 文件。
type SQLiteFile struct {
	ID          string `db:"id" json:"id"`
	WorkspaceID string `db:"workspace_id" json:"workspace_id"`
	Name        string `db:"name" json:"name"`
	Size        int64  `db:"size" json:"size"`
	CreatedBy   string `db:"created_by" json:"created_by"`
	CreatedAt   int64  `db:"created_at" json:"created_at"`
}

func (s *Service) hSQLiteList(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
		return err
	}
	var out []SQLiteFile
	if err := s.st.Select(r.Context(), &out, `SELECT * FROM uploaded_sqlite WHERE workspace_id = ? ORDER BY created_at DESC`, ws); err != nil {
		return err
	}
	for i := range out {
		if fi, err := os.Stat(filepath.Join(s.sqlite.UploadDir, out[i].ID+".db")); err == nil {
			out[i].Size = fi.Size()
		}
	}
	if out == nil {
		out = []SQLiteFile{}
	}
	return web.OK(w, map[string]any{"files": out})
}

func (s *Service) hSQLiteUpload(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
		return err
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		return apperr.Bad("请选择文件")
	}
	defer file.Close()
	head := make([]byte, 16)
	if n, _ := io.ReadFull(file, head); n != 16 || string(head) != "SQLite format 3\x00" {
		return apperr.Bad("不是 SQLite 3 数据库文件")
	}
	if err := os.MkdirAll(s.sqlite.UploadDir, 0o700); err != nil {
		return err
	}
	id := ids.New()
	path := filepath.Join(s.sqlite.UploadDir, id+".db")
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := out.Write(head)
	if err == nil {
		var m int64
		m, err = io.Copy(out, file)
		n += int(m)
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
		return apperr.Bad("上传失败：%v", err)
	}
	name := strings.TrimSpace(filepath.Base(hdr.Filename))
	if name == "" || len([]rune(name)) > 200 {
		name = id + ".db"
	}
	f := &SQLiteFile{ID: id, WorkspaceID: ws, Name: name, Size: int64(n), CreatedBy: web.UserOf(r).ID, CreatedAt: store.Now()}
	if err := s.st.Add(r.Context(), "uploaded_sqlite", f); err != nil {
		os.Remove(path)
		return err
	}
	srv.AuditR(r, audit.Entry{Action: "sqlite.upload", WorkspaceID: ws, Target: name}, map[string]any{"size": n})
	return web.OK(w, map[string]any{"file": f, "ref": "upload:" + id})
}

func (s *Service) hSQLiteDelete(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
		return err
	}
	if err := srv.RequireReauth(r); err != nil {
		return err
	}
	var f SQLiteFile
	if err := s.st.Get(r.Context(), &f, `SELECT * FROM uploaded_sqlite WHERE id = ? AND workspace_id = ?`, chi.URLParam(r, "id"), ws); err != nil {
		return apperr.NotFound("文件")
	}
	var used int
	_ = s.st.GetW(r.Context(), &used, `SELECT COUNT(*) FROM connections WHERE driver = 'sqlite' AND params LIKE ?`, "%upload:"+f.ID+"%")
	if used > 0 {
		return apperr.Conflict("还有连接在使用这个文件，请先删除这些连接")
	}
	if _, err := s.st.Exec(r.Context(), `DELETE FROM uploaded_sqlite WHERE id = ?`, f.ID); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(s.sqlite.UploadDir, f.ID+".db"))
	srv.AuditR(r, audit.Entry{Action: "sqlite.delete", WorkspaceID: ws, Target: f.Name}, nil)
	return web.OK(w, map[string]any{"ok": true})
}

// hSQLiteDirs 列出白名单目录及其中的 SQLite 文件（只看一层子目录，最多 500 个）。
func (s *Service) hSQLiteDirs(srv *web.Server, w http.ResponseWriter, r *http.Request) error {
	ws := chi.URLParam(r, "ws")
	if _, err := srv.RequireWS(r, ws, accounts.WSAdmin); err != nil {
		return err
	}
	type entry struct {
		Path string `json:"path"`
		Size int64  `json:"size"`
	}
	type dir struct {
		Path  string  `json:"path"`
		Files []entry `json:"files"`
		Error string  `json:"error,omitempty"`
	}
	out := []dir{}
	for _, d := range s.sqlite.AllowedDirs {
		dd := dir{Path: d, Files: []entry{}}
		count := 0
		_ = filepath.WalkDir(d, func(p string, de os.DirEntry, err error) error {
			if err != nil {
				if p == d {
					dd.Error = err.Error()
				}
				return nil
			}
			if de.IsDir() {
				if p != d && strings.Count(strings.TrimPrefix(p, d), string(filepath.Separator)) > 2 {
					return filepath.SkipDir
				}
				return nil
			}
			ext := strings.ToLower(filepath.Ext(p))
			if ext != ".db" && ext != ".sqlite" && ext != ".sqlite3" && ext != ".db3" {
				return nil
			}
			if count >= 500 {
				return filepath.SkipAll
			}
			if fi, err := de.Info(); err == nil && fi.Mode().IsRegular() {
				dd.Files = append(dd.Files, entry{Path: p, Size: fi.Size()})
				count++
			}
			return nil
		})
		sort.Slice(dd.Files, func(i, j int) bool { return dd.Files[i].Path < dd.Files[j].Path })
		out = append(out, dd)
	}
	return web.OK(w, map[string]any{"dirs": out})
}
