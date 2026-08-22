// Package sshconfig parses ~/.ssh/config files to resolve host aliases.
package sshconfig

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Entry is the resolved configuration for a host alias.
type Entry struct {
	HostName     string
	User         string
	Port         int
	IdentityFile string
}

type hostBlock struct {
	patterns []string
	config   map[string]string
}

// Lookup resolves a host alias from the SSH config file. When configFilePath
// is empty, the default ~/.ssh/config is used and a missing file returns nil.
func Lookup(hostAlias, configFilePath string) (*Entry, error) {
	configPath := configFilePath
	if configPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		configPath = filepath.Join(home, ".ssh", "config")
	}

	if _, err := os.Stat(configPath); err != nil {
		if configFilePath == "" {
			return nil, nil
		}
		return nil, err
	}

	blocks, err := parseConfigFile(configPath, map[string]bool{})
	if err != nil {
		return nil, err
	}
	return matchHost(hostAlias, blocks), nil
}

// parseConfigFile parses an SSH config file, following Include directives.
func parseConfigFile(filePath string, visited map[string]bool) ([]hostBlock, error) {
	realPath, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		realPath = filePath
	}
	if visited[realPath] {
		return nil, nil
	}
	visited[realPath] = true

	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}

	var blocks []hostBlock
	var current *hostBlock

	for _, rawLine := range strings.Split(string(content), "\n") {
		line := rawLine
		if idx := strings.Index(line, "#"); idx != -1 {
			line = line[:idx]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "include ") {
			if current != nil {
				blocks = append(blocks, *current)
				current = nil
			}
			pattern := strings.TrimSpace(line[len("include "):])
			includePaths := expandIncludePath(pattern, filepath.Dir(filePath))
			for _, includePath := range includePaths {
				if _, err := os.Stat(includePath); err == nil {
					included, err := parseConfigFile(includePath, visited)
					if err != nil {
						return nil, err
					}
					blocks = append(blocks, included...)
				}
			}
			continue
		}

		if strings.HasPrefix(lower, "host ") {
			if current != nil {
				blocks = append(blocks, *current)
			}
			current = &hostBlock{
				patterns: strings.Fields(line[len("host "):]),
				config:   map[string]string{},
			}
			continue
		}

		if current == nil {
			current = &hostBlock{
				patterns: []string{"*"},
				config:   map[string]string{},
			}
		}

		spaceIdx := strings.IndexAny(line, " \t")
		if spaceIdx == -1 {
			continue
		}
		key := strings.ToLower(line[:spaceIdx])
		value := strings.TrimSpace(line[spaceIdx+1:])
		// SSH first-match-wins: keep only the first value per key.
		if _, ok := current.config[key]; !ok {
			current.config[key] = value
		}
	}

	if current != nil {
		blocks = append(blocks, *current)
	}
	return blocks, nil
}

// expandIncludePath expands ~ and glob patterns in an Include directive.
func expandIncludePath(pattern, baseDir string) []string {
	switch {
	case strings.HasPrefix(pattern, "~/"):
		home, _ := os.UserHomeDir()
		pattern = filepath.Join(home, pattern[2:])
	case strings.HasPrefix(pattern, "~"):
		return nil
	case !filepath.IsAbs(pattern):
		pattern = filepath.Join(baseDir, pattern)
	}

	if !strings.ContainsAny(pattern, "*?") {
		return []string{pattern}
	}

	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	return matches
}

// matchHost resolves an alias against parsed blocks using first-match-wins.
func matchHost(hostAlias string, blocks []hostBlock) *Entry {
	result := &Entry{}
	for _, block := range blocks {
		if !hostBlockMatches(hostAlias, block.patterns) {
			continue
		}
		if result.HostName == "" {
			result.HostName = block.config["hostname"]
		}
		if result.User == "" {
			result.User = block.config["user"]
		}
		if result.Port == 0 {
			if n, err := strconv.Atoi(block.config["port"]); err == nil {
				result.Port = n
			}
		}
		if result.IdentityFile == "" {
			result.IdentityFile = expandTilde(block.config["identityfile"])
		}
	}
	if result.HostName == "" && result.User == "" && result.Port == 0 && result.IdentityFile == "" {
		return nil
	}
	return result
}

func hostBlockMatches(hostAlias string, patterns []string) bool {
	positiveMatch := false
	for _, pattern := range patterns {
		isNegated := strings.HasPrefix(pattern, "!")
		body := pattern
		if isNegated {
			body = pattern[1:]
		}
		if body == "" {
			continue
		}
		if hostPatternMatches(hostAlias, body) {
			if isNegated {
				return false
			}
			positiveMatch = true
		}
	}
	return positiveMatch
}

func hostPatternMatches(hostAlias, pattern string) bool {
	if pattern == "*" {
		return true
	}
	var sb strings.Builder
	sb.WriteString("^")
	for _, r := range pattern {
		switch r {
		case '*':
			sb.WriteString(".*")
		case '?':
			sb.WriteString(".")
		default:
			sb.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	sb.WriteString("$")
	re, err := regexp.Compile(sb.String())
	if err != nil {
		return false
	}
	return re.MatchString(hostAlias)
}

func expandTilde(p string) string {
	if p == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}