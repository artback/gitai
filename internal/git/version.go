package git

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// ExtractVersionFromDiff scans a unified diff for lines that look like version updates.
func ExtractVersionFromDiff(diffText string) string {
	lines := strings.Split(diffText, "\n")
	versionRegex := regexp.MustCompile(`([0-9]+\.[0-9][0-9a-z.-]*)`)

	var oldVersion, newVersion string
	var currentFile string
	var isVerFile bool

	for _, line := range lines {
		// Resilience: Use a prefix check for diff headers
		if strings.HasPrefix(line, "diff --git") {
			// Extract filename from the end of the line (e.g., "b/composer.json")
			parts := strings.Fields(line)
			if len(parts) > 0 {
				rawPath := parts[len(parts)-1]
				currentFile = filepath.Base(rawPath)
				isVerFile = isVersionFile(currentFile)
				// Reset versions for a new file block
				oldVersion, newVersion = "", ""
			}
			continue
		}

		// Optimization: Skip if not in a version-related file
		if !isVerFile {
			continue
		}

		lowerLine := strings.ToLower(line)
		isExplicitVersionFile := strings.EqualFold(currentFile, "VERSION")
		containsVersionKeyword := strings.Contains(lowerLine, "version") && !strings.Contains(lowerLine, "versioning")

		// If it's a "VERSION" file or the line mentions "version"
		if isExplicitVersionFile || containsVersionKeyword {
			if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
				if matches := versionRegex.FindStringSubmatch(line[1:]); len(matches) > 1 {
					oldVersion = matches[1]
				}
			}
			if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
				if matches := versionRegex.FindStringSubmatch(line[1:]); len(matches) > 1 {
					newVersion = matches[1]
				}
			}
		}

		// Return transition if both found in this file block
		if oldVersion != "" && newVersion != "" && oldVersion != newVersion {
			return fmt.Sprintf("%s -> %s", oldVersion, newVersion)
		}
	}

	// For new files (like your "Initial version" test case), oldVersion will be empty.
	return newVersion
}

func isVersionFile(filename string) bool {
	f := strings.ToLower(filename)
	if strings.Contains(f, "test") || strings.Contains(f, "_spec") {
		return false
	}
	targets := []string{
		"version", "package.json", "go.mod", "cargo.toml", "pyproject.toml",
		"composer.json", "gemfile", "mix.exs", "version.rb", "version.py",
		"setup.py", "cmakelists.txt",
	}
	for _, t := range targets {
		if strings.EqualFold(f, t) {
			return true
		}
	}
	return false
}
