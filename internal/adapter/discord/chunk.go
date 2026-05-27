package discord

import "github.com/punny/espur/internal/adapter/textchunk"

// chunk delegates to the shared textchunk package. Kept as a thin wrapper
// so the Discord adapter can keep its package-local helper name.
func chunk(body string, maxLen int) []string {
	return textchunk.Split(body, maxLen)
}
