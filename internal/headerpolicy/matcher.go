package headerpolicy

import (
	"regexp"
	"strings"
	"sync"

	"github.com/fangxiusun/ai-adapter/internal/config"
)

// regexCache caches compiled wildcard and regex patterns.
var (
	regexCacheMu sync.RWMutex
	regexCache   = make(map[string]*regexp.Regexp)
)

// getOrCompileRegex returns a compiled regex from cache or compiles and caches it.
func getOrCompileRegex(pattern string) (*regexp.Regexp, error) {
	regexCacheMu.RLock()
	if re, ok := regexCache[pattern]; ok {
		regexCacheMu.RUnlock()
		return re, nil
	}
	regexCacheMu.RUnlock()

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}

	regexCacheMu.Lock()
	regexCache[pattern] = re
	regexCacheMu.Unlock()

	return re, nil
}

// MatchExact performs a case-insensitive exact match on header key.
func MatchExact(pattern, key string) bool {
	return strings.EqualFold(pattern, key)
}

// MatchWildcard matches header key against a wildcard pattern.
// The wildcard character * can appear anywhere in the pattern.
// Examples: X-*-ID, *-Type, prefix-*, X-Internal-*
func MatchWildcard(pattern, key string) bool {
	// Convert wildcard pattern to regex: * -> .*
	// Lowercase the pattern for case-insensitive matching
	lowerPattern := strings.ToLower(pattern)
	regexPattern := "^" + regexp.QuoteMeta(lowerPattern) + "$"
	regexPattern = strings.ReplaceAll(regexPattern, `\*`, `.*`)

	re, err := getOrCompileRegex(regexPattern)
	if err != nil {
		// Fallback to exact match if pattern is invalid
		return strings.EqualFold(pattern, key)
	}

	return re.MatchString(strings.ToLower(key))
}

// MatchRegex matches header key against a regex pattern.
func MatchRegex(pattern, key string) bool {
	re, err := getOrCompileRegex(pattern)
	if err != nil {
		return false
	}
	return re.MatchString(strings.ToLower(key))
}

// Match dispatches to the appropriate matcher based on match type.
// Header key is always lowercased before matching.
func Match(matchType config.HeaderMatchType, pattern, key string) bool {
	lowerKey := strings.ToLower(key)
	switch matchType {
	case config.MatchExact:
		return MatchExact(pattern, lowerKey)
	case config.MatchWildcard:
		return MatchWildcard(pattern, lowerKey)
	case config.MatchRegex:
		return MatchRegex(pattern, lowerKey)
	default:
		return false
	}
}
