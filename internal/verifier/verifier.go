package verifier

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// VerifySHA256 必验，不对直接中断（调用方映射退出码 4）。
func VerifySHA256(path, want string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if !equalHex(got, want) {
		return fmt.Errorf("sha256 不匹配: 期待 %s，实际 %s", want, got)
	}
	return nil
}

// VerifyCosign 可选验：official 强制签名场景由调用方传入 identity。
// 若本机装有 cosign 则真实校验，否则返回明确错误（不静默跳过）。
func VerifyCosign(path, sigFile, identity string) error {
	if sigFile == "" && identity == "" {
		return nil
	}
	bin, err := exec.LookPath("cosign")
	if err != nil {
		return fmt.Errorf("需要 cosign 校验但本机未安装 cosign")
	}
	args := []string{"verify-blob", "--signature", sigFile, path}
	if identity != "" {
		args = append(args, "--certificate-identity", identity)
	}
	cmd := exec.Command(bin, args...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("cosign 校验失败: %w", err)
	}
	return nil
}

func equalHex(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		c1, c2 := a[i], b[i]
		if 'A' <= c1 && c1 <= 'F' {
			c1 += 'a' - 'A'
		}
		if 'A' <= c2 && c2 <= 'F' {
			c2 += 'a' - 'A'
		}
		if c1 != c2 {
			return false
		}
	}
	return true
}
