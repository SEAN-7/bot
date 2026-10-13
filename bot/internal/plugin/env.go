package plugin

import (
	"fmt"
	"sort"
	"strings"
)

// ParseEnv reads KEY=value lines; blank lines and # comments are ignored.
func ParseEnv(data []byte) (map[string]string, error) {
	env := map[string]string{}
	for i, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("line %d: expected KEY=value", i+1)
		}
		env[key] = value
	}
	return env, nil
}

func FormatEnv(env map[string]string) []byte {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, env[k])
	}
	return []byte(b.String())
}
