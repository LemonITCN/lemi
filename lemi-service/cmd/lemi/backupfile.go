package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/LemonITCN/lemi/lemi-service/internal/backup/cryptstream"
	"github.com/klauspost/compress/gzip"
	"github.com/klauspost/compress/zstd"
	"golang.org/x/term"
)

func init() {
	commands["backup-file"] = command{Usage: "backup-file decrypt|to-sql <备份文件> [-o 输出] [--passphrase-file 文件]   离线解密 / 解压备份（不需要 Lemi 数据目录）", Run: runBackupFile}
}

const backupFileUsage = `用法：
  lemi backup-file decrypt <备份文件> [-o 输出] [--passphrase-file 文件]
      解密 .lemienc，得到压缩后的原文件（例如 x.sql.zst）
  lemi backup-file to-sql <备份文件> [-o 输出] [--passphrase-file 文件]
      解密（需要时）并解压，得到可直接导入的 .sql（SQLite 文件快照得到 .db）
  -o 输出文件，- 表示标准输出；默认去掉相应扩展名后写在原文件旁边，不覆盖已有文件
  --passphrase-file 从文件读取备份密码（去掉结尾换行）；不给时在终端无回显输入`

type backupFileArgs struct {
	sub, input, output, passFile string
}

func parseBackupFileArgs(args []string) (*backupFileArgs, error) {
	if len(args) == 0 {
		return nil, errors.New(backupFileUsage)
	}
	a := &backupFileArgs{sub: args[0]}
	if a.sub != "decrypt" && a.sub != "to-sql" {
		return nil, fmt.Errorf("未知的子命令 %q\n\n%s", a.sub, backupFileUsage)
	}
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		val := func(name string) (string, error) {
			if k, v, ok := strings.Cut(arg, "="); ok && strings.HasPrefix(k, "-") {
				return v, nil
			}
			if i+1 >= len(rest) {
				return "", fmt.Errorf("%s 需要一个参数", name)
			}
			i++
			return rest[i], nil
		}
		var err error
		switch {
		case arg == "-o" || arg == "--output" || strings.HasPrefix(arg, "-o=") || strings.HasPrefix(arg, "--output="):
			a.output, err = val("-o")
		case arg == "--passphrase-file" || arg == "-passphrase-file" || strings.HasPrefix(arg, "--passphrase-file="):
			a.passFile, err = val("--passphrase-file")
		case arg == "-h" || arg == "--help":
			return nil, errors.New(backupFileUsage)
		case strings.HasPrefix(arg, "-") && arg != "-":
			return nil, fmt.Errorf("未知的选项 %q\n\n%s", arg, backupFileUsage)
		default:
			if a.input != "" {
				return nil, errors.New("只能指定一个备份文件")
			}
			a.input = arg
		}
		if err != nil {
			return nil, err
		}
	}
	if a.input == "" {
		return nil, errors.New("请指定备份文件\n\n" + backupFileUsage)
	}
	return a, nil
}

func runBackupFile(args []string) error {
	a, err := parseBackupFileArgs(args)
	if err != nil {
		return err
	}
	return backupFile(a, os.Stdout, os.Stderr, readPassphraseTerminal)
}

func readPassphraseTerminal() (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return "", errors.New("这是加密备份，请用 --passphrase-file 提供备份密码")
	}
	fmt.Fprint(os.Stderr, "备份密码：")
	b, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func readPassphraseFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取口令文件失败：%w", err)
	}
	return strings.TrimRight(string(b), "\r\n"), nil
}

var (
	magicZstd   = []byte{0x28, 0xb5, 0x2f, 0xfd}
	magicGzip   = []byte{0x1f, 0x8b}
	magicSQLite = []byte("SQLite format 3\x00")
)

func backupFile(a *backupFileArgs, stdout, stderr io.Writer, askPass func() (string, error)) (err error) {
	in, err := os.Open(a.input)
	if err != nil {
		return err
	}
	defer in.Close()
	br := bufio.NewReaderSize(in, 1<<20)
	head, _ := br.Peek(len(cryptstream.Magic))
	encrypted := cryptstream.IsEncrypted(head)
	var src io.Reader = br
	if encrypted {
		pass := ""
		if a.passFile != "" {
			if pass, err = readPassphraseFile(a.passFile); err != nil {
				return err
			}
		} else if pass, err = askPass(); err != nil {
			return err
		}
		r, err := cryptstream.NewReader(br, pass)
		if err != nil {
			return err
		}
		src = r
	} else if a.sub == "decrypt" {
		return errors.New("这个文件没有加密（不是 .lemienc），不需要解密")
	}
	base := filepath.Base(a.input)
	outName := strings.TrimSuffix(base, cryptstream.Ext)
	if a.sub == "to-sql" {
		dbr := bufio.NewReaderSize(src, 1<<20)
		h, _ := dbr.Peek(4)
		switch {
		case bytes.HasPrefix(h, magicZstd):
			d, err := zstd.NewReader(dbr)
			if err != nil {
				return err
			}
			defer d.Close()
			src = d
		case bytes.HasPrefix(h, magicGzip):
			g, err := gzip.NewReader(dbr)
			if err != nil {
				return err
			}
			defer g.Close()
			src = g
		default:
			src = dbr
		}
		outName = strings.TrimSuffix(strings.TrimSuffix(outName, ".zst"), ".gz")
		pr := bufio.NewReaderSize(src, 1<<20)
		h, _ = pr.Peek(len(magicSQLite))
		if bytes.Equal(h, magicSQLite) {
			if !strings.HasSuffix(outName, ".db") {
				outName += ".db"
			}
			fmt.Fprintln(stderr, "提示：这是 SQLite 文件快照，输出的是 .db 数据库文件而不是 SQL。")
		} else if !strings.HasSuffix(outName, ".sql") {
			outName += ".sql"
		}
		src = pr
	}
	if outName == base {
		outName += ".out"
	}
	if a.output == "-" {
		if _, err := io.Copy(stdout, src); err != nil {
			return fmt.Errorf("处理失败（输出的内容不完整，请丢弃）：%w", err)
		}
		return nil
	}
	out := a.output
	if out == "" {
		out = filepath.Join(filepath.Dir(a.input), outName)
	}
	if _, err := os.Stat(out); err == nil {
		return fmt.Errorf("输出文件 %s 已存在，不覆盖", out)
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), ".lemi-backup-file-*")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tmp.Close()
			os.Remove(tmp.Name())
		}
	}()
	n, err := io.Copy(tmp, src)
	if err != nil {
		return fmt.Errorf("处理失败：%w", err)
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), out); err != nil {
		return err
	}
	_ = os.Chmod(out, 0o600)
	fmt.Fprintf(stderr, "已写出 %s（%d 字节）\n", out, n)
	return nil
}
