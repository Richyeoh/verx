package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// sparseClone 把大仓以最小成本拉到本地，只检出 sparsePaths。
// 用 --depth 1 + --filter=blob:none，winget-pkgs / vcpkg / scoop 桶都适用，
// 全程走 git 协议，零 GitHub API 配额。
func sparseClone(repo, dest string, sparsePaths []string) error {
	if st, err := os.Stat(filepath.Join(dest, ".git")); err == nil && st.IsDir() {
		// 已存在：尝试更新到最新，失败也不致命（用本地旧快照继续跑）
		cmd := exec.Command("git", "-C", dest, "pull", "--ff-only", "--depth", "1")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("git pull %s 失败（用本地快照继续）: %v %s\n", repo, err, strings.TrimSpace(string(out)))
			return nil
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	args := []string{"clone", "--depth", "1", "--filter=blob:none", "--sparse", "https://github.com/" + repo, dest}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		// 老 git 不支持 --filter：退化为普通浅克隆
		fmt.Printf("sparse clone 不支持 --filter，退化为 --depth 1: %v %s\n", err, strings.TrimSpace(string(out)))
		os.RemoveAll(dest)
		args2 := []string{"clone", "--depth", "1", "https://github.com/" + repo, dest}
		if out2, err2 := exec.Command("git", args2...).CombinedOutput(); err2 != nil {
			return fmt.Errorf("git clone %s 失败: %v %s", repo, err2, strings.TrimSpace(string(out2)))
		}
	}
	if len(sparsePaths) > 0 {
		setArgs := append([]string{"-C", dest, "sparse-checkout", "set"}, sparsePaths...)
		if out, err := exec.Command("git", setArgs...).CombinedOutput(); err != nil {
			return fmt.Errorf("sparse-checkout 失败: %v %s", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// cloneNoCheckout 只拉 commit + tree，不检出任何 blob，适合 winget-pkgs 这种
// 百万文件级大仓：枚举用 git ls-tree（本地操作，零 API），文件内容走 CDN 按需拉。
func cloneNoCheckout(repo, dest string) error {
	if st, err := os.Stat(filepath.Join(dest, ".git")); err == nil && st.IsDir() {
		cmd := exec.Command("git", "-C", dest, "fetch", "--depth", "1", "origin", "HEAD")
		if out, err := cmd.CombinedOutput(); err != nil {
			fmt.Printf("git fetch %s 失败（用本地快照继续）: %v %s\n", repo, err, strings.TrimSpace(string(out)))
			return nil
		}
		if out, err := exec.Command("git", "-C", dest, "reset", "--hard", "FETCH_HEAD").CombinedOutput(); err != nil {
			fmt.Printf("git reset %s 失败: %v %s\n", repo, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	// --no-checkout + --filter=blob:none：只拿提交和目录树，不下任何文件内容
	args := []string{"clone", "--depth", "1", "--filter=blob:none", "--no-checkout", "https://github.com/" + repo, dest}
	if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
		fmt.Printf("no-checkout clone 不支持 --filter，退化为 --no-checkout: %v %s\n", err, strings.TrimSpace(string(out)))
		os.RemoveAll(dest)
		args2 := []string{"clone", "--depth", "1", "--no-checkout", "https://github.com/" + repo, dest}
		if out2, err2 := exec.Command("git", args2...).CombinedOutput(); err2 != nil {
			return fmt.Errorf("git clone %s 失败: %v %s", repo, err2, strings.TrimSpace(string(out2)))
		}
	}
	return nil
}

// gitLsTreeDirs 返回 HEAD 下 dir（形如 "manifests"）的直接子目录名，纯本地操作。
func gitLsTreeDirs(gitDir, dir string) ([]string, error) {
	out, err := exec.Command("git", "-C", gitDir, "ls-tree", "-d", "--name-only", "HEAD", dir).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree %s 失败: %v %s", dir, err, strings.TrimSpace(string(out)))
	}
	var names []string
	for _, line := range strings.Split(string(out), "\n") {
		n := strings.TrimSpace(line)
		if n != "" {
			// ls-tree 返回全路径，取最后一段
			if i := strings.LastIndex(n, "/"); i >= 0 {
				n = n[i+1:]
			}
			names = append(names, n)
		}
	}
	return names, nil
}

// gitLsTreeAllDirs 递归列出 HEAD 下 root 的全部目录（相对路径），一次调用，
// 适合解析 manifests/<letter>/<pub>/<pkg> 三层结构，避免 N+1 次 git 调用。
func gitLsTreeAllDirs(gitDir, root string) ([]string, error) {
	out, err := exec.Command("git", "-C", gitDir, "ls-tree", "-r", "-d", "--name-only", "HEAD", root).CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git ls-tree -r %s 失败: %v %s", root, err, strings.TrimSpace(string(out)))
	}
	var dirs []string
	for _, line := range strings.Split(string(out), "\n") {
		d := strings.TrimSpace(line)
		if d != "" {
			dirs = append(dirs, d)
		}
	}
	return dirs, nil
}

// cacheDir 返回本地克隆根目录，可用 VERX_IMPORTER_CACHE 覆盖（CI 可做 actions/cache）。
func cacheDir() string {
	if d := strings.TrimSpace(os.Getenv("VERX_IMPORTER_CACHE")); d != "" {
		return d
	}
	return filepath.Join(os.TempDir(), "verx-importer")
}
