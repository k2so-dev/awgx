package doctor

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

func ApplyFixes(ctx context.Context, env *Env, ids []string, log func(string)) []string {
	return Apply(ctx, Run(ctx, env), ids, log)
}

func Apply(ctx context.Context, results []Result, ids []string, log func(string)) []string {
	var failed []string
	for _, r := range results {
		base, _, _ := strings.Cut(r.ID, ":")
		if r.Fix == nil || r.Status == OK || (!slices.Contains(ids, r.ID) && !slices.Contains(ids, base)) {
			continue
		}
		log("fix: " + r.Fix.Description)
		if err := r.Fix.Apply(ctx); err != nil {
			failed = append(failed, fmt.Sprintf("fix %s failed: %v", r.ID, err))
		}
	}
	return failed
}
