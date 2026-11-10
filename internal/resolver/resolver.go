package resolver

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/verx/verx/internal/manifest"
	"github.com/verx/verx/internal/registry"
	ver "github.com/verx/verx/internal/version"
)

// Node 求解结果一包。
type Node struct {
	Registry string
	Manifest *manifest.Manifest
}

func splitQuery(q string) (name, wantVer string) {
	// registry/name@ver、name@ver、name
	if i := strings.LastIndex(q, "@"); i >= 0 {
		// 排除 registry/name 里没有 @ 的情况；@ 后必须非空才算版本
		if i+1 < len(q) {
			return q[:i], q[i+1:]
		}
	}
	return q, ""
}

// Resolve SAT-lite：range/provides/conflicts/replaces。
// 返回安装顺序（依赖在前）。--dry-run 到此为止由调用方保证。
// query 支持 name、name@version、registry/name、registry/name@version。
func Resolve(query string) ([]Node, error) {
	var order []Node
	visited := map[string]bool{}   // name@version 已排
	chosen := map[string]string{}  // name -> version 已选（冲突检测用）
	providing := map[string]string{} // 虚拟提供 -> 包名
	replaced := map[string]bool{}   // 被 replaces 替代掉的包名

	var visit func(q string, stack []string) error
	visit = func(q string, stack []string) error {
		namePart, wantVer := splitQuery(q)
		cand, err := registry.PickVer(namePart, wantVer)
		if err != nil {
			return err
		}
		m := cand.Manifest
		key := m.Name + "@" + m.Version
		if visited[key] {
			return nil
		}
		for _, s := range stack {
			if s == m.Name {
				return fmt.Errorf("依赖循环: %s", strings.Join(append(stack, m.Name), " -> "))
			}
		}
		if !m.Supports(runtime.GOOS, runtime.GOARCH) {
			return fmt.Errorf("包 %s 不支持当前平台 %s/%s", m.Name, runtime.GOOS, runtime.GOARCH)
		}
		// conflicts：与已选包名/虚拟提供冲突则报
		for _, c := range m.Conflicts {
			if _, ok := chosen[c]; ok {
				return fmt.Errorf("依赖冲突: %s 与已选 %s 冲突", m.Name, c)
			}
			if owner, ok := providing[c]; ok {
				return fmt.Errorf("依赖冲突: %s 与已选提供 %s(%s) 冲突", m.Name, c, owner)
			}
		}
		// replaces：声明替代旧包，若旧包已选则踢掉（upgrade 语义），否则记为已替代
		for _, r := range m.Replaces {
			replaced[r] = true
			// 从 order 里移除已被选的旧包
			for i, n := range order {
				if n.Manifest.Name == r {
					order = append(order[:i], order[i+1:]...)
					delete(visited, n.Manifest.Name+"@"+n.Manifest.Version)
					delete(chosen, r)
					break
				}
			}
		}
		if replaced[m.Name] {
			return fmt.Errorf("依赖冲突: %s 已被 %s 替代", m.Name, "已选包")
		}
		stack = append(stack, m.Name)
		for _, d := range m.Depends {
			if len(d.OS) > 0 {
				hit := false
				for _, o := range d.OS {
					if o == runtime.GOOS {
						hit = true
						break
					}
				}
				if !hit {
					continue
				}
			}
			// 依赖先看虚拟提供/包名是否已被满足
			if owner, ok := providing[d.Name]; ok {
				if d.Range == "" {
					continue
				}
				// 找到提供者的版本再校验
				for _, n := range order {
					if n.Manifest.Name == owner {
						if !ver.Satisfies(n.Manifest.Version, d.Range) {
							return fmt.Errorf("依赖冲突: %s 需要 %s %s，实际 %s(%s 提供)", m.Name, d.Name, d.Range, n.Manifest.Version, owner)
						}
						break
					}
				}
				continue
			}
			if v, ok := chosen[d.Name]; ok {
				if d.Range != "" && !ver.Satisfies(v, d.Range) {
					return fmt.Errorf("依赖冲突: %s 需要 %s %s，实际 %s", m.Name, d.Name, d.Range, v)
				}
				continue
			}
			// replaces 满足依赖：已选包 replaces 了该依赖名
			satisfied := false
			for _, n := range order {
				for _, r := range n.Manifest.Replaces {
					if r == d.Name {
						satisfied = true
						break
					}
				}
			}
			if satisfied {
				continue
			}
			dc, err := registry.PickVer(d.Name, "")
			if err != nil {
				return fmt.Errorf("依赖 %s 找不到: %w", d.Name, err)
			}
			if d.Range != "" && !ver.Satisfies(dc.Manifest.Version, d.Range) {
				return fmt.Errorf("依赖冲突: %s 需要 %s %s，实际 %s", m.Name, d.Name, d.Range, dc.Manifest.Version)
			}
			if err := visit(dc.Manifest.Name, stack); err != nil {
				return err
			}
		}
		for _, p := range m.Provides {
			if owner, ok := providing[p]; ok && owner != m.Name {
				return fmt.Errorf("虚拟提供冲突: %s 已由 %s 提供", p, owner)
			}
			providing[p] = m.Name
		}
		visited[key] = true
		chosen[m.Name] = m.Version
		order = append(order, Node{Registry: cand.Meta.Name, Manifest: m})
		return nil
	}
	if err := visit(query, nil); err != nil {
		return nil, err
	}
	return order, nil
}
