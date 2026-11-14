// login 从妙手（CatPaw）桌面客户端本地凭据中解出 Passport token 并导入账号池。
//
// 原理：客户端把 Meituan SSO token 用 AES-256-GCM 加密写在
//
//	macOS:  ~/Library/Application Support/catpaw-moon/catx-credential.json
//	Win:    %APPDATA%\catpaw-moon\catx-credential.json
//
// 密钥 = sha256("<machineId>:catpaw-desk-token-v2")，其中 machineId 为
// macOS 的 IOPlatformUUID（小写）或 Windows 的 MachineGuid。
//
// 用法：
//
//	go run ./cmd/login                      # 自动读取本机客户端登录态
//	go run ./cmd/login -token <passport>    # 直接导入一个 token
//	go run ./cmd/login -check               # 只校验已有账号是否有效
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/linquin/longcat2api/internal/account"
	"github.com/linquin/longcat2api/internal/config"
	"github.com/linquin/longcat2api/internal/upstream"
)

func main() {
	cfgPath := flag.String("config", "config.json", "配置文件路径")
	token := flag.String("token", "", "直接导入 Passport token（跳过本机凭据解密）")
	credPath := flag.String("credential", "", "catx-credential.json 路径（默认自动探测）")
	name := flag.String("name", "", "账号备注名")
	check := flag.Bool("check", false, "仅校验已有账号")
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}
	store, err := account.Open(cfg.AccountsFile)
	if err != nil {
		log.Fatalf("加载账号失败: %v", err)
	}
	up := upstream.New(cfg)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if *check {
		for _, a := range store.List() {
			uid, uname, err := up.CheckToken(ctx, a.Token)
			if err != nil {
				log.Printf("❌ %s(%s): %v", a.ID, a.Name, err)
				continue
			}
			log.Printf("✅ %s uid=%s user=%s 本地记录 uid=%s", a.ID, uid, uname, a.UID)
		}
		return
	}

	tk := strings.TrimSpace(*token)
	if tk == "" {
		p := *credPath
		if p == "" {
			p = defaultCredentialPath()
		}
		var err error
		tk, err = readTokenFromClient(p)
		if err != nil {
			log.Fatalf("读取客户端登录态失败: %v", err)
		}
		log.Printf("已从 %s 解出 token（%d 字节）", p, len(tk))
	}

	uid, uname, err := up.CheckToken(ctx, tk)
	if err != nil {
		log.Fatalf("token 校验失败: %v", err)
	}
	label := *name
	if label == "" {
		label = uname
	}
	acc := &account.Account{
		Name:    label,
		Token:   tk,
		UID:     uid,
		Enabled: true,
		Note:    "imported by login",
	}
	if err := store.Add(acc); err != nil {
		log.Fatalf("写入账号失败: %v", err)
	}
	log.Printf("✅ 已导入账号 uid=%s user=%s → %s", uid, uname, cfg.AccountsFile)
}

func defaultCredentialPath() string {
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library/Application Support/catpaw-moon/catx-credential.json")
	case "windows":
		return filepath.Join(os.Getenv("APPDATA"), "catpaw-moon", "catx-credential.json")
	default:
		return filepath.Join(home, ".config/catpaw-moon/catx-credential.json")
	}
}

type ssoCredential struct {
	SSOTokenEnc string `json:"ssoTokenEnc"`
}

func readTokenFromClient(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", path, err)
	}
	var c ssoCredential
	if err := json.Unmarshal(raw, &c); err != nil {
		return "", fmt.Errorf("解析凭据文件失败: %w", err)
	}
	if c.SSOTokenEnc == "" {
		return "", fmt.Errorf("凭据文件里没有 ssoTokenEnc（客户端未登录？）")
	}
	mid, err := machineID()
	if err != nil {
		return "", err
	}
	return decrypt(mid, c.SSOTokenEnc)
}

// machineID 返回加密用的机器标识（与客户端 node-machine-id 一致）。
func machineID() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
		if err != nil {
			return "", fmt.Errorf("读取 IOPlatformUUID 失败: %w", err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "IOPlatformUUID") {
				parts := strings.Split(line, "=")
				if len(parts) < 2 {
					continue
				}
				return strings.ToLower(strings.Trim(strings.TrimSpace(parts[1]), `"`)), nil
			}
		}
		return "", fmt.Errorf("未找到 IOPlatformUUID")
	case "windows":
		out, err := exec.Command("reg", "QUERY", `HKEY_LOCAL_MACHINE\SOFTWARE\Microsoft\Cryptography`, "/v", "MachineGuid").Output()
		if err != nil {
			return "", fmt.Errorf("读取 MachineGuid 失败: %w", err)
		}
		f := strings.Fields(string(out))
		if len(f) == 0 {
			return "", fmt.Errorf("MachineGuid 为空")
		}
		return strings.TrimSpace(f[len(f)-1]), nil
	default:
		return "", fmt.Errorf("当前系统 %s 暂未支持自动解密，请用 -token 手工导入", runtime.GOOS)
	}
}

// decrypt 解开 ssoTokenEnc，返回 token 字符串。
func decrypt(machineID, encB64 string) (string, error) {
	key := sha256.Sum256([]byte(machineID + ":catpaw-desk-token-v2"))
	buf, err := base64.StdEncoding.DecodeString(encB64)
	if err != nil {
		return "", fmt.Errorf("密文 base64 解码失败: %w", err)
	}
	if len(buf) < 12+16+1 {
		return "", fmt.Errorf("密文长度异常")
	}
	iv := buf[:12]
	tag := buf[12:28]
	data := buf[28:]
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	plain, err := gcm.Open(nil, iv, append(append([]byte{}, data...), tag...), nil)
	if err != nil {
		return "", fmt.Errorf("解密失败（机器标识不匹配或凭据已损坏）: %w", err)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(plain, &payload); err != nil {
		return "", fmt.Errorf("解密后内容解析失败: %w", err)
	}
	if payload.AccessToken == "" {
		return "", fmt.Errorf("解密后没有 access_token")
	}
	return payload.AccessToken, nil
}
