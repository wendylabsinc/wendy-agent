// Package buildargs validates KEY=VALUE build arguments headed for a builder
// CLI.
//
// It lives in shared/ rather than in the CLI because the agent's remote build
// service must re-validate what a client sends: the CLI's own validation is a
// convenience for the developer, not a security boundary, and a second copy of
// these rules would be a copy that drifts.
package buildargs

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

var validBuildArgNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidatePair gates a KEY=VALUE build arg headed for a builder CLI.
// Values are user-authored (docker-compose args, wendy.json) and legitimately
// hold spaces, slashes, and other punctuation — a Minecraft MOTD or a log path,
// say. Their content is not dangerous: each pair is handed to exec.Command as a
// single "KEY=VALUE" argv element (no shell is involved), and because KEY is
// validated to [A-Za-z_][A-Za-z0-9_]* the token always starts with a letter, so
// a builder CLI can never mistake the value for a flag. So the rejections are
// only:
//   - a leading '-', kept as defense-in-depth so a value can never look like a
//     flag even if a future call site passed it as a standalone argv token; and
//   - control characters, which are genuinely unsafe regardless: NUL truncates
//     C strings (Go's exec rejects it outright) and CR/LF/escape sequences can
//     inject lines or terminal escapes into the streamed build log.
func ValidatePair(key, value string) error {
	if !validBuildArgNameRe.MatchString(key) {
		return fmt.Errorf("invalid build arg name %q: must match [A-Za-z_][A-Za-z0-9_]*", key)
	}
	if strings.HasPrefix(value, "-") {
		return fmt.Errorf("invalid build arg %q: value must not start with '-'", key)
	}
	if strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid build arg %q: value must not contain control characters", key)
	}
	return nil
}

// SortedValidatedKeys validates every pair and returns the keys in sorted
// order, so a build command line is reproducible across runs. An invalid pair
// fails the whole set rather than being dropped: silently omitting a build arg
// would produce an image that differs from the one the developer described.
func SortedValidatedKeys(buildArgs map[string]string) ([]string, error) {
	keys := make([]string, 0, len(buildArgs))
	for k, v := range buildArgs {
		if err := ValidatePair(k, v); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys, nil
}
