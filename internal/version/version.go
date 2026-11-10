package version

import (
	"strconv"
	"strings"
)

// Compare 比较版本：SemVer 优先 + revision，返回 -1/0/1。
func Compare(a, b string) int {
	pa, ra := split(a)
	pb, rb := split(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			if pa[i] < pb[i] {
				return -1
			}
			return 1
		}
	}
	if ra != rb {
		if ra < rb {
			return -1
		}
		return 1
	}
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}

// Satisfies 判断 version 是否满足 range，支持 =、^、~、>=、>、<=、<，逗号分隔全满足。
func Satisfies(ver, rng string) bool {
	rng = strings.TrimSpace(rng)
	if rng == "" || rng == "*" {
		return true
	}
	for _, c := range strings.Split(rng, ",") {
		if !satisfyOne(ver, strings.TrimSpace(c)) {
			return false
		}
	}
	return true
}

func satisfyOne(ver, cond string) bool {
	var op, want string
	for _, p := range []string{">=", "<=", "^", "~", ">", "<", "="} {
		if strings.HasPrefix(cond, p) {
			op = p
			want = strings.TrimSpace(strings.TrimPrefix(cond, p))
			break
		}
	}
	if op == "" {
		op = "="
		want = cond
	}
	cmp := Compare(ver, want)
	switch op {
	case "=":
		return cmp == 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "^":
		// ^1.2.3 := >=1.2.3 <2.0.0；^0.2.3 := >=0.2.3 <0.3.0；^0.0.3 := =0.0.3
		if cmp < 0 {
			return false
		}
		pw := split3(want)
		pv := split3(ver)
		if pw[0] > 0 {
			return pv[0] == pw[0]
		}
		if pw[1] > 0 {
			return pv[0] == 0 && pv[1] == pw[1]
		}
		return Compare(ver, want) == 0
	case "~":
		// ~1.2.3 := >=1.2.3 <1.3.0
		if cmp < 0 {
			return false
		}
		pw := split3(want)
		pv := split3(ver)
		return pv[0] == pw[0] && pv[1] == pw[1]
	}
	return false
}

func split(v string) ([3]int, int) {
	var nums [3]int
	rev := 0
	core := v
	if i := strings.Index(core, "+"); i >= 0 {
		core = core[:i]
	}
	if i := strings.Index(core, "-"); i >= 0 {
		if n, err := strconv.Atoi(core[i+1:]); err == nil {
			rev = n
		}
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	for i := 0; i < 3 && i < len(parts); i++ {
		n, _ := strconv.Atoi(strings.TrimSpace(parts[i]))
		nums[i] = n
	}
	return nums, rev
}

func split3(v string) [3]int {
	n, _ := split(v)
	return n
}
