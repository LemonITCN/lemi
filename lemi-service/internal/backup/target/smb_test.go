package target

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudsoda/go-smb2"
)

func TestSMBConfigErrors(t *testing.T) {
	bad := []SMBConfig{
		{Share: "s", Username: "u"},
		{Host: "h", Username: "u"},
		{Host: "h", Share: "s"},
		{Host: "h", Share: "a/b", Username: "u"},
		{Host: "h", Share: "s", Username: "u", Port: 70000},
		{Host: "h", Share: "s", Username: "u", Path: "../x"},
		{Host: `h\x`, Share: "s", Username: "u"},
	}
	for _, c := range bad {
		b, _ := json.Marshal(c)
		if _, err := Open(KindSMB, b, Deps{}); err == nil {
			t.Errorf("配置 %+v 应报错", c)
		}
	}
	b, _ := json.Marshal(SMBConfig{Host: "nas", Share: `\backup\`, Username: "u", Path: "/a//b/"})
	tg, err := Open(KindSMB, b, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	st := tg.(*smbTarget)
	if st.addr != "nas:445" || st.share != "backup" || st.base != "a/b" || st.smbPath("x/y") != "a/b/x/y" {
		t.Fatalf("SMB 配置解析不对：%+v", st)
	}
}

// smbTempFiles 递归找共享里的临时文件（List 会跳过它们，这里直接读目录）。
func smbTempFiles(t *testing.T, tg *smbTarget) []string {
	t.Helper()
	var found []string
	err := tg.with(context.Background(), true, func(fsys *smb2.Share) error {
		var walk func(dir string) error
		walk = func(dir string) error {
			infos, err := fsys.ReadDir(dir)
			if err != nil {
				return err
			}
			for _, fi := range infos {
				child := joinKey(dir, fi.Name())
				if dir == "" {
					child = fi.Name()
				}
				if fi.IsDir() {
					if err := walk(child); err != nil {
						return err
					}
				} else if strings.HasPrefix(fi.Name(), tempPrefix) {
					found = append(found, child)
				}
			}
			return nil
		}
		return walk(tg.base)
	})
	if err != nil {
		t.Fatalf("读共享目录失败：%v", err)
	}
	return found
}

// 真实 SMB 服务器测试：设置 LEMI_TEST_SMB_ADDR（主机:端口）/ SHARE / USER / PASSWORD 时运行。
// 本地可以用 Docker 起一个 Samba，例如（镜像前缀按本机网络环境调整）：
//
//	docker run -d --name lemi-test-smb -p 127.0.0.1:1445:445 dperson/samba -u "lemi;<口令>" -s "backup;/share;yes;no;no;lemi"
func TestSMBReal(t *testing.T) {
	addr := os.Getenv("LEMI_TEST_SMB_ADDR")
	if addr == "" {
		t.Skip("未设置 LEMI_TEST_SMB_ADDR，跳过真实 SMB 测试")
	}
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(portStr)
	c := SMBConfig{Host: host, Port: port, Share: os.Getenv("LEMI_TEST_SMB_SHARE"), Username: os.Getenv("LEMI_TEST_SMB_USER"),
		Password: os.Getenv("LEMI_TEST_SMB_PASSWORD"), Path: "lemi-test-" + randomHex(4) + "/备份 目录"}
	var dc dialCounter
	b, _ := json.Marshal(c)
	tg, err := Open(KindSMB, b, Deps{Dial: dc.dial})
	if err != nil {
		t.Fatal(err)
	}
	defer tg.Close()
	st := tg.(*smbTarget)
	runSuite(t, tg, suiteOpts{bigSize: 3<<20 + 11})
	runStreamSuite(t, tg, streamOpts{bigSize: 3<<20 + 9, abortAt: 1 << 20, noLeftover: func(t *testing.T) {
		eventually(t, "临时文件被清理", func() bool { return len(smbTempFiles(t, st)) == 0 })
	}})
	if dc.n.Load() == 0 {
		t.Fatal("SMB 应经 Deps.Dial 拨号")
	}
	// 断线后自动重连。
	st.dropCurrent()
	mustPut(t, tg, "after-drop.txt", []byte("ok"))
	if got := mustGet(t, tg, "after-drop.txt"); string(got) != "ok" {
		t.Fatalf("重连后读取不对：%q", got)
	}
	// 共享不存在。
	missing := c
	missing.Share = "no-such-share"
	b, _ = json.Marshal(missing)
	ms, _ := Open(KindSMB, b, Deps{})
	if err := Probe(context.Background(), ms); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("共享不存在提示不对：%v", err)
	}
	// 口令错误。
	c.Password = "wrong-password"
	b, _ = json.Marshal(c)
	bad, _ := Open(KindSMB, b, Deps{})
	if err := Probe(context.Background(), bad); err == nil || !strings.Contains(err.Error(), "不正确") || strings.Contains(err.Error(), "wrong-password") {
		t.Fatalf("口令错误提示不对：%v", err)
	}
	// 清理：删掉测试文件与目录。
	ctx := context.Background()
	for _, p := range listPaths(t, tg, "") {
		if err := tg.Delete(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	st.with(ctx, true, func(fsys *smb2.Share) error {
		return fsys.RemoveAll(strings.SplitN(st.base, "/", 2)[0])
	})
}
