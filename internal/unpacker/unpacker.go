package unpacker

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Unpack 解包到 dest（临时目录），拒绝 ../、绝对路径、逃逸 symlink。
// 支持 zip/tar.gz 自解，tar.xz/7z 调系统工具（tar/7z），dmg/pkg 需确认挂载，msi/exe 由 installer 走静默安装。
func Unpack(archive, dest string, strip int) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	lower := strings.ToLower(archive)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		return unzip(archive, dest, strip)
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		return untarGz(archive, dest, strip)
	case strings.HasSuffix(lower, ".tar.xz") || strings.HasSuffix(lower, ".txz"):
		if err := untarSystem(archive, dest, strip); err != nil {
			return fmt.Errorf("tar.xz 解包失败（需系统 tar）: %w", err)
		}
		return nil
	case strings.HasSuffix(lower, ".7z"):
		if err := un7zSystem(archive, dest); err != nil {
			return fmt.Errorf("7z 解包失败（需系统 7z）: %w", err)
		}
		return nil
	case strings.HasSuffix(lower, ".dmg") || strings.HasSuffix(lower, ".pkg"):
		return fmt.Errorf("dmg/pkg 需用户确认挂载（installer 平台杂务处理）")
	case strings.HasSuffix(lower, ".msi") || strings.HasSuffix(lower, ".exe"):
		return fmt.Errorf("msi/exe 由 installer 按 installer.silent 静默安装，不走解包")
	default:
		return fmt.Errorf("未知压缩格式: %s", archive)
	}
}

func safeJoin(dest, name string, strip int) (string, error) {
	if filepath.IsAbs(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("非法路径: %q", name)
	}
	parts := strings.Split(filepath.ToSlash(name), "/")
	if strip > 0 {
		if len(parts) <= strip {
			return "", nil
		}
		parts = parts[strip:]
	}
	rel := filepath.FromSlash(strings.Join(parts, "/"))
	target := filepath.Join(dest, rel)
	if r, err := filepath.Rel(dest, target); err != nil || strings.HasPrefix(r, "..") {
		return "", fmt.Errorf("路径逃逸: %q", name)
	}
	return target, nil
}

// untarSystem 调系统 tar 解 tar.xz（*nix 自带，Windows 10+ 自带 tar.exe）。
func untarSystem(archive, dest string, strip int) error {
	args := []string{"-xf", archive, "-C", dest}
	if strip > 0 {
		args = append(args, fmt.Sprintf("--strip-components=%d", strip))
	}
	cmd := exec.Command("tar", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("tar: %v\n%s", err, string(out))
	}
	return nil
}

// un7zSystem 调系统 7z 解包。
func un7zSystem(archive, dest string) error {
	for _, bin := range []string{"7z", "7zz"} {
		cmd := exec.Command(bin, "x", archive, "-o"+dest, "-y")
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	cmd := exec.Command("7z", "x", archive, "-o"+dest, "-y")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("7z: %v\n%s", err, string(out))
	}
	return nil
}

func unzip(archive, dest string, strip int) error {
	r, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer r.Close()
	for _, f := range r.File {
		target, err := safeJoin(dest, f.Name, strip)
		if err != nil {
			return err
		}
		if target == "" {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		_, err = io.Copy(out, rc)
		out.Close()
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func untarGz(archive, dest string, strip int) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Linkname != "" {
			if filepath.IsAbs(hdr.Linkname) || strings.Contains(hdr.Linkname, "..") {
				return fmt.Errorf("非法 symlink: %q", hdr.Name)
			}
		}
		target, err := safeJoin(dest, hdr.Name, strip)
		if err != nil {
			return err
		}
		if target == "" {
			continue
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return err
			}
			out.Close()
		}
	}
	return nil
}
