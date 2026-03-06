package main

import "path"

// filterEntityIDsByGlob applies include/exclude glob filters to entity IDs.
// Include filters are evaluated first (if provided), then exclude filters.
func filterEntityIDsByGlob(entityIDs []string, includePatterns, excludePatterns []string) []string {
	if len(includePatterns) == 0 && len(excludePatterns) == 0 {
		return entityIDs
	}

	filtered := make([]string, 0, len(entityIDs))
	for _, id := range entityIDs {
		if len(includePatterns) > 0 && !matchesAnyGlob(id, includePatterns) {
			continue
		}
		if len(excludePatterns) > 0 && matchesAnyGlob(id, excludePatterns) {
			continue
		}
		filtered = append(filtered, id)
	}

	return filtered
}

func matchesAnyGlob(value string, patterns []string) bool {
	for _, pattern := range patterns {
		matched, err := path.Match(pattern, value)
		if err != nil {
			continue
		}
		if matched {
			return true
		}
	}
	return false
}
