package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/verx/verx/internal/manifest"
)

// validateDir 校验 outDir/manifests/<name>/<name>.yaml 全部能过 manifest.Validate。
// 供 CI 在提 PR 前拦截坏包，本地也可以跑：
//   go run ./tools/importer validate --dir /tmp/registry-out
func validateDir(dir string) error {
	base := filepath.Join(dir, "manifests")
	entries, err := os.ReadDir(base)
	if err != nil {
		return fmt.Errorf("读 %s 失败: %w", base, err)
	}
	ok, fail := 0, 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		p := filepath.Join(base, e.Name(), e.Name()+".yaml")
		if _, err := manifest.LoadFile(p, e.Name()); err != nil {
			fmt.Printf("FAIL %s: %v\n", e.Name(), err)
			fail++
			continue
		}
		fmt.Printf("OK %s\n", e.Name())
		ok++
	}
	fmt.Printf("\nvalidate: 成功 %d，失败 %d\n", ok, fail)
	if fail > 0 {
		return fmt.Errorf("validate %d 个失败", fail)
	}
	if ok == 0 {
		return fmt.Errorf("validate 0 个包")
	}
	return nil
}
