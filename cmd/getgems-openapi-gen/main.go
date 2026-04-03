package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

const (
	specURL          = "https://api.getgems.io/public-api/docs.json"
	generatorVersion = "v2.5.1"
	configRelPath    = "internal/getgems/openapi/oapi-codegen.yaml"
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

	tmpFile, err := os.CreateTemp("", "getgems-openapi-*.json")
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

	if err := tmpFile.Close(); err != nil {
		cleanup()
		return "", nil, err
	}

	return tmpFile.Name(), cleanup, nil
}

func exitf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
