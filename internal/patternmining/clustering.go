package patternmining

import (
	"sort"
)

// editClosures groups the unique items into edit-distance closures, one per distinct member set.
func (m *Miner) editClosures(items []string, delta int) [][]string {
	sorted := append([]string(nil), items...)
	sort.Strings(sorted)
	var uniq []string
	for k, s := range sorted {
		if k == 0 || s != sorted[k-1] {
			uniq = append(uniq, s)
		}
	}
	ids := make([]int, len(uniq))
	for k, s := range uniq {
		id, ok := m.index[s]
		if !ok {
			id = -1
		}
		ids[k] = id
	}
	var ret [][]string
	seen := make(map[string]bool)
	var members []int
	var buf []byte
	for ai := range uniq {
		members = members[:0]
		for bi := range uniq {
			if bi == ai || m.distIdx(ids[ai], ids[bi]) < delta {
				members = append(members, bi)
			}
		}
		buf = buf[:0]
		for _, bi := range members {
			buf = append(buf, uniq[bi]...)
			buf = append(buf, 0)
		}
		if seen[string(buf)] {
			continue
		}
		seen[string(buf)] = true
		r := make([]string, len(members))
		for k, bi := range members {
			r[k] = uniq[bi]
		}
		ret = append(ret, r)
	}
	return ret
}

