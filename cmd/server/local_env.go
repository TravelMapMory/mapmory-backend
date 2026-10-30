package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// loadLocalEnv reads the repository-root .env for local server settings.
// Explicit process environment variables take precedence. Only server settings
// are imported; Firebase test credentials and ID tokens stay in the file for
// the token helper and REST Client, not in the server's environment.
//
// This is deliberately a small KEY=value reader, not a shell interpreter.
// It supports unquoted and matching single- or double-quoted values.
func loadLocalEnv(path string) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil // Deployed environments supply process variables instead.
	}
	if err != nil {
		return fmt.Errorf("open local configuration: %w", err)
	}
	defer file.Close()

	allowed := map[string]bool{
		"FIREBASE_PROJECT_ID":               true,
		"MAPMORY_ENABLE_DB_USER_RESOLUTION": true,
		"DATABASE_URL":                      true,
	}
	values := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for line := 1; scanner.Scan(); line++ {
		entry := strings.TrimSpace(scanner.Text())
		if entry == "" || strings.HasPrefix(entry, "#") {
			continue
		}
		key, raw, ok := strings.Cut(entry, "=")
		if !ok {
			continue // Ignore unrelated editor annotations.
		}
		key = strings.TrimSpace(key)
		if !allowed[key] {
			continue
		}
		value := strings.TrimSpace(raw)
		if len(value) > 0 && (value[0] == '\'' || value[0] == '"') {
			quote := value[0]
			if len(value) < 2 || value[len(value)-1] != quote {
				return fmt.Errorf("invalid quoted value for %s at line %d", key, line)
			}
			value = value[1 : len(value)-1]
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read local configuration: %w", err)
	}

	for key, value := range values {
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return fmt.Errorf("set local configuration %s: %w", key, err)
			}
		}
	}
	return nil
}
