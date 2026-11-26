package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LemonITCN/lemi/lemi-service/internal/backup/cryptstream"
	"github.com/klauspost/compress/zstd"
)

func makeBackup(t *testing.T, dir, name, pass string, plain []byte) string {
	t.Helper()
	var buf bytes.Buffer
	var w io.Writer = &buf
	var enc io.WriteCloser
	if pass != "" {
		var err error
		enc, err = cryptstream.NewWriterParams(&buf, pass, cryptstream.Params{Time: 1, MemoryKiB: 64, Threads: 1})
		if err != nil {
			t.Fatal(err)
		}
		w = enc
	}
	z, _ := zstd.NewWriter(w)
	z.Write(plain)
	z.Close()
	if enc != nil {
		enc.Close()
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestBackupFileToSQL(t *testing.T) {
	dir := t.TempDir()
	plain := []byte("-- Lemi Dump SQL\nCREATE TABLE t (id INT);\nINSERT INTO t VALUES (1);\n")
	in := makeBackup(t, dir, "app-main-20261126-120000.sql.zst.lemienc", "correct horse battery", plain)
	pf := filepath.Join(dir, "pass.txt")
	os.WriteFile(pf, []byte("correct horse battery\n"), 0o600)
	noAsk := func() (string, error) { return "", errors.New("不应询问") }

	a, err := parseBackupFileArgs([]string{"to-sql", in, "--passphrase-file", pf})
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	if err := backupFile(a, io.Discard, &stderr, noAsk); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "app-main-20261126-120000.sql"))
	if err != nil || !bytes.Equal(got, plain) {
		t.Fatalf("to-sql 结果不对：%v", err)
	}
	// 再来一次：不覆盖已有文件
	if err := backupFile(a, io.Discard, &stderr, noAsk); err == nil || !strings.Contains(err.Error(), "已存在") {
		t.Fatalf("应拒绝覆盖：%v", err)
	}
	// decrypt：只解密，得到 .sql.zst
	a, _ = parseBackupFileArgs([]string{"decrypt", "--passphrase-file=" + pf, in})
	if err := backupFile(a, io.Discard, &stderr, noAsk); err != nil {
		t.Fatal(err)
	}
	zst, _ := os.ReadFile(filepath.Join(dir, "app-main-20261126-120000.sql.zst"))
	d, _ := zstd.NewReader(bytes.NewReader(zst))
	dec, _ := io.ReadAll(d)
	if !bytes.Equal(dec, plain) {
		t.Fatal("decrypt 结果不对")
	}
	// 输出到标准输出，口令从「终端」读
	a, _ = parseBackupFileArgs([]string{"to-sql", in, "-o", "-"})
	var out bytes.Buffer
	if err := backupFile(a, &out, &stderr, func() (string, error) { return "correct horse battery", nil }); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), plain) {
		t.Fatal("标准输出内容不对")
	}
}

func TestBackupFileErrors(t *testing.T) {
	dir := t.TempDir()
	plain := bytes.Repeat([]byte("INSERT INTO t VALUES (1);\n"), 100000)
	in := makeBackup(t, dir, "x.sql.zst.lemienc", "correct horse battery", plain)
	ask := func(p string) func() (string, error) { return func() (string, error) { return p, nil } }
	a, _ := parseBackupFileArgs([]string{"to-sql", in})
	if err := backupFile(a, io.Discard, io.Discard, ask("wrong")); !errors.Is(err, cryptstream.ErrBadPassphrase) {
		t.Fatalf("错误口令：%v", err)
	}
	// 截断的文件：报错且不留下输出文件
	b, _ := os.ReadFile(in)
	trunc := filepath.Join(dir, "y.sql.zst.lemienc")
	os.WriteFile(trunc, b[:len(b)-100], 0o600)
	a, _ = parseBackupFileArgs([]string{"to-sql", trunc})
	if err := backupFile(a, io.Discard, io.Discard, ask("correct horse battery")); err == nil {
		t.Fatal("截断的文件应报错")
	}
	ents, _ := os.ReadDir(dir)
	for _, e := range ents {
		if strings.HasPrefix(e.Name(), "y.sql") && e.Name() != "y.sql.zst.lemienc" || strings.HasPrefix(e.Name(), ".lemi-backup-file-") {
			t.Fatalf("留下了不完整的输出：%s", e.Name())
		}
	}
	// 未加密文件不能 decrypt，但能 to-sql
	plainFile := makeBackup(t, dir, "z.sql.zst", "", []byte("SELECT 1;\n"))
	a, _ = parseBackupFileArgs([]string{"decrypt", plainFile})
	if err := backupFile(a, io.Discard, io.Discard, ask("")); err == nil {
		t.Fatal("未加密文件 decrypt 应报错")
	}
	a, _ = parseBackupFileArgs([]string{"to-sql", plainFile})
	if err := backupFile(a, io.Discard, io.Discard, ask("")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "z.sql")); string(b) != "SELECT 1;\n" {
		t.Fatal("未加密文件 to-sql 结果不对")
	}
	for _, bad := range [][]string{nil, {"foo", "x"}, {"to-sql"}, {"to-sql", "a", "b"}, {"to-sql", "a", "--bogus"}, {"to-sql", "a", "-o"}} {
		if _, err := parseBackupFileArgs(bad); err == nil {
			t.Errorf("%v 应报错", bad)
		}
	}
}
