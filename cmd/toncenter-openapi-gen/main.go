package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	specURL          = "https://toncenter.com/api/v2/openapi.json"
	generatorVersion = "v2.5.1"
	configRelPath    = "internal/toncenter/openapi/oapi-codegen.yaml"
)

func main() {
	repoRoot, err := repoRoot()
	if err != nil {
		exitf("resolve repo root: %v", err)
	}

	specPath, cleanup, err := downloadSpec()
	if err != nil {
		exitf("download OpenAPI spec: %v", err)
	}
	defer cleanup()

	configPath := filepath.Join(repoRoot, configRelPath)

	cmd := exec.Command(
		"go",
		"run",
		"github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@"+generatorVersion,
		"-config",
		configPath,
		specPath,
	)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		exitf("run oapi-codegen: %v", err)
	}
}

func repoRoot() (string, error) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("runtime.Caller failed")
	}

	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..")), nil
}

func downloadSpec() (string, func(), error) {
	resp, err := http.Get(specURL)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	tmpFile, err := os.CreateTemp("", "toncenter-openapi-*.json")
	if err != nil {
		return "", nil, err
	}

	cleanup := func() {
		_ = os.Remove(tmpFile.Name())
	}

	if _, err := io.Copy(tmpFile, resp.Body); err != nil {
		_ = tmpFile.Close()
		cleanup()
		return "", nil, err
	}

	if err := normalizeSpec(tmpFile.Name()); err != nil {
		_ = tmpFile.Close()
		cleanup()
		return "", nil, err
	}

	if err := tmpFile.Close(); err != nil {
		cleanup()
		return "", nil, err
	}

	return tmpFile.Name(), cleanup, nil
}

func normalizeSpec(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	var spec map[string]any
	if err := json.Unmarshal(data, &spec); err != nil {
		return err
	}

	if version, ok := spec["openapi"].(string); ok && version == "3.1.1" {
		spec["openapi"] = "3.0.3"
	}

	components, ok := spec["components"].(map[string]any)
	if !ok {
		return nil
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		return nil
	}

	if _, exists := schemas["TransactionsStdRequest"]; !exists {
		if _, hasBase := schemas["TransactionsRequest"]; hasBase {
			schemas["TransactionsStdRequest"] = map[string]any{
				"$ref": "#/components/schemas/TransactionsRequest",
			}
		}
	}

	ensureRequiredProperty(schemas, "MsgDataRaw", "@type")
	ensureRequiredProperty(schemas, "MsgDataText", "@type")
	ensureRequiredProperty(schemas, "MsgDataDecryptedText", "@type")
	ensureRequiredProperty(schemas, "MsgDataEncryptedText", "@type")

	normalizeNode(spec)

	normalized, err := json.Marshal(spec)
	if err != nil {
		return err
	}

	return os.WriteFile(path, normalized, 0o600)
}

func normalizeNode(node any) {
	switch current := node.(type) {
	case map[string]any:
		if typ, ok := current["type"].(string); ok && isSchemaRef(typ) {
			delete(current, "type")
			current["$ref"] = typ
		}
		for _, value := range current {
			normalizeNode(value)
		}
	case []any:
		for _, value := range current {
			normalizeNode(value)
		}
	}
}

func isSchemaRef(value string) bool {
	return len(value) > len("#/components/schemas/") && value[:len("#/components/schemas/")] == "#/components/schemas/"
}

func ensureRequiredProperty(schemas map[string]any, schemaName, property string) {
	schema, ok := schemas[schemaName].(map[string]any)
	if !ok {
		return
	}

	required, ok := schema["required"].([]any)
	if !ok {
		schema["required"] = []any{property}
		return
	}

	for _, entry := range required {
		if value, ok := entry.(string); ok && value == property {
			return
		}
	}

	schema["required"] = append(required, property)
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
