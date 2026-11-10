package fetcher

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func verxHome() string {
	if v := os.Getenv("VERX_HOME"); v != "" {
		return v
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return ".verx"
	}
	return filepath.Join(h, ".verx")
}

func downloadsDir() string { return filepath.Join(verxHome(), "cache", "downloads") }

func fileURLToPath(url string) string {
	p := strings.TrimPrefix(url, "file://")
	if len(p) >= 3 && p[0] == '/' && ((p[1] >= 'A' && p[1] <= 'Z') || (p[1] >= 'a' && p[1] <= 'z')) && p[2] == ':' {
		p = p[1:]
	}
	return filepath.FromSlash(p)
}

// Fetch 下载 url 到 cache/downloads，支持断点续传与镜像 fallback。
// offline 只用缓存。返回本地文件路径。
func Fetch(name, version, goos, goarch, url string, mirrors []string, offline bool) (string, error) {
	ext := filepath.Ext(strings.Split(url, "?")[0])
	if strings.HasSuffix(strings.ToLower(url), ".tar.gz") {
		ext = ".tar.gz"
	}
	dst := filepath.Join(downloadsDir(), fmt.Sprintf("%s-%s-%s-%s%s", name, version, goos, goarch, ext))
	if offline {
		if _, err := os.Stat(dst); err == nil {
			return dst, nil
		}
		return "", fmt.Errorf("离线模式且无缓存: %s", dst)
	}
	if strings.HasPrefix(url, "file://") {
		src := fileURLToPath(url)
		data, err := os.ReadFile(src)
		if err != nil {
			return "", err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return "", err
		}
		return dst, os.WriteFile(dst, data, 0o644)
	}
	urls := append([]string{url}, mirrors...)
	var last error
	for _, u := range urls {
		if err := downloadResume(u, dst); err != nil {
			last = err
			continue
		}
		return dst, nil
	}
	return "", fmt.Errorf("下载失败: %v", last)
}

func downloadResume(url, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	var start int64
	if st, err := os.Stat(dst); err == nil {
		start = st.Size()
	}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	if start > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		return nil
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("http %d: %s", resp.StatusCode, url)
	}
	flag := os.O_CREATE | os.O_WRONLY
	if resp.StatusCode == http.StatusPartialContent {
		flag |= os.O_APPEND
	} else {
		flag |= os.O_TRUNC
	}
	f, err := os.OpenFile(dst, flag, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}
