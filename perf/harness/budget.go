package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Budget caps the median of a metric for one cluster size.
type Budget struct {
	Size   string  `json:"size"`
	Metric string  `json:"metric"`
	Median float64 `json:"median"`
}

// checkBudgets returns one message per metric whose median exceeds its budget.
func checkBudgets(path string, stats []Stat) []string {
	bb, err := os.ReadFile(path)
	if err != nil {
		return []string{err.Error()}
	}

	var budgets []Budget
	if err := json.Unmarshal(bb, &budgets); err != nil {
		return []string{err.Error()}
	}

	idx := make(map[string]Stat, len(stats))
	for _, s := range stats {
		idx[s.Size+"/"+s.Metric] = s
	}

	var out []string
	for _, b := range budgets {
		s, ok := idx[b.Size+"/"+b.Metric]
		if !ok {
			continue
		}
		if s.Median > b.Median {
			out = append(out, fmt.Sprintf("%s/%s median %.1f%s > budget %.1f", b.Size, b.Metric, s.Median, s.Unit, b.Median))
		}
	}

	return out
}
