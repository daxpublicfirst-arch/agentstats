package transcript

import "sort"

// Sorting helpers shared by the session summary. Every list is sorted by
// count descending, then name ascending, so output is deterministic.

func topRepeated(counts map[string]int, limit int) []RepeatedCmd {
	type kv struct {
		k string
		v int
	}
	var items []kv
	for k, v := range counts {
		if v > 1 {
			items = append(items, kv{k, v})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].v != items[j].v {
			return items[i].v > items[j].v
		}
		return items[i].k < items[j].k
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]RepeatedCmd, 0, len(items))
	for _, it := range items {
		out = append(out, RepeatedCmd{Command: it.k, Count: it.v})
	}
	return out
}

func topFiles(counts map[string]int, limit int) []FileCount {
	type kv struct {
		k string
		v int
	}
	var items []kv
	for k, v := range counts {
		items = append(items, kv{k, v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].v != items[j].v {
			return items[i].v > items[j].v
		}
		return items[i].k < items[j].k
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]FileCount, 0, len(items))
	for _, it := range items {
		out = append(out, FileCount{Count: it.v, Path: it.k})
	}
	return out
}

func topErrors(counts map[string]int, tools, msgs map[string]string, limit int) []ErrorCount {
	type kv struct {
		k string
		v int
	}
	var items []kv
	for k, v := range counts {
		items = append(items, kv{k, v})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].v != items[j].v {
			return items[i].v > items[j].v
		}
		if tools[items[i].k] != tools[items[j].k] {
			return tools[items[i].k] < tools[items[j].k]
		}
		return msgs[items[i].k] < msgs[items[j].k]
	})
	if len(items) > limit {
		items = items[:limit]
	}
	out := make([]ErrorCount, 0, len(items))
	for _, it := range items {
		out = append(out, ErrorCount{Count: it.v, Message: msgs[it.k], Tool: tools[it.k]})
	}
	return out
}
