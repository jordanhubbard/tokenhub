package httpapi

import (
	"strings"

	"github.com/jordanhubbard/tokenhub/internal/router"
)

func normalizeClientModelHint(engine *router.Engine, hint string) string {
	hint = strings.TrimSpace(hint)
	if hint == "" || router.IsWildcardModelHint(hint) {
		return hint
	}

	if idx := strings.IndexByte(hint, '/'); idx > 0 && engine != nil {
		bare := hint[idx+1:]
		if engine.HasModel(bare) {
			return bare
		}
	}
	return hint
}
