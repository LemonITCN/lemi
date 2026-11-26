// releasesign 是 Lemi 发布签名工具，维护者与 CI 用，不随程序发布。
//
//	go run ./internal/selfupdate/releasesign keygen <目录>
//	    生成 Ed25519 密钥对：<目录>/release-signing.key（32 字节种子的 base64，0600）与 release-signing.pub（公钥 base64）。
//	    已有文件时拒绝覆盖。生成后把公钥写进 internal/selfupdate/pubkey.go，私钥放 GitHub Secrets LEMI_RELEASE_SIGNING_KEY。
//	LEMI_RELEASE_SIGNING_KEY=<种子 base64> go run ./internal/selfupdate/releasesign sign <文件>
//	    写出 <文件>.sig。私钥对应的公钥必须与程序内置公钥一致，否则报错（防止用错密钥发出无法升级的版本）。
//	go run ./internal/selfupdate/releasesign verify <文件> [签名文件]
//	    用程序内置公钥校验（签名文件默认 <文件>.sig）。
//	go run ./internal/selfupdate/releasesign pubkey
//	    打印程序内置公钥（base64 与 PEM，PEM 可配合 openssl 手工校验）。
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/LemonITCN/lemi/lemi-service/internal/selfupdate"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("用法：releasesign keygen <目录> | sign <文件> | verify <文件> [签名文件] | pubkey")
	}
	switch args[0] {
	case "keygen":
		if len(args) != 2 {
			return errors.New("用法：releasesign keygen <目录>")
		}
		return keygen(args[1])
	case "sign":
		if len(args) != 2 {
			return errors.New("用法：releasesign sign <文件>")
		}
		return sign(args[1])
	case "verify":
		if len(args) != 2 && len(args) != 3 {
			return errors.New("用法：releasesign verify <文件> [签名文件]")
		}
		sig := args[1] + ".sig"
		if len(args) == 3 {
			sig = args[2]
		}
		return verify(args[1], sig)
	case "pubkey":
		return printPub(selfupdate.ReleasePublicKey())
	}
	return fmt.Errorf("未知子命令：%s", args[0])
}

func keygen(dir string) error {
	keyPath := filepath.Join(dir, "release-signing.key")
	pubPath := filepath.Join(dir, "release-signing.pub")
	for _, p := range []string{keyPath, pubPath} {
		if _, err := os.Stat(p); err == nil {
			return fmt.Errorf("%s 已存在，拒绝覆盖（换密钥会让已安装的旧版本无法在线升级）", p)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	if err := os.WriteFile(keyPath, []byte(base64.StdEncoding.EncodeToString(priv.Seed())+"\n"), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(pubPath, []byte(base64.StdEncoding.EncodeToString(pub)+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Printf("已生成：\n  私钥 %s\n  公钥 %s\n", keyPath, pubPath)
	return printPub(pub)
}

func sign(file string) error {
	v := os.Getenv("LEMI_RELEASE_SIGNING_KEY")
	if v == "" {
		return errors.New("缺少环境变量 LEMI_RELEASE_SIGNING_KEY")
	}
	key, err := selfupdate.ParseSigningKey(v)
	if err != nil {
		return err
	}
	if !bytes.Equal(key.Public().(ed25519.PublicKey), selfupdate.ReleasePublicKey()) {
		return errors.New("签名私钥与程序内置公钥（internal/selfupdate/pubkey.go）不匹配，拒绝签名")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	if err := os.WriteFile(file+".sig", selfupdate.SignSums(key, data), 0o644); err != nil {
		return err
	}
	fmt.Printf("已签名：%s.sig\n", file)
	return nil
}

func verify(file, sigFile string) error {
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(sigFile)
	if err != nil {
		return err
	}
	if err := selfupdate.VerifySums(selfupdate.ReleasePublicKey(), data, sig); err != nil {
		return err
	}
	fmt.Printf("签名有效：%s\n", file)
	return nil
}

func printPub(pub ed25519.PublicKey) error {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return err
	}
	fmt.Printf("公钥（base64）：%s\n\n", base64.StdEncoding.EncodeToString(pub))
	return pem.Encode(os.Stdout, &pem.Block{Type: "PUBLIC KEY", Bytes: der})
}
