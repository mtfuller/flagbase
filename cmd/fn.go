package cmd

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// fnProjectConfig mirrors the .flagbase.json file written into every scaffold.
type fnProjectConfig struct {
	Server     string `json:"server"`
	FunctionID string `json:"function_id"`
	Name       string `json:"name"`
}

var (
	fnServer string
	fnToken  string
)

var fnCmd = &cobra.Command{
	Use:   "fn",
	Short: "Manage serverless functions",
	Long: `Create, build, and deploy serverless functions.

JavaScript functions are interpreted in-process on the server. Go functions
are compiled locally to WebAssembly (WASI preview1) and uploaded as a binary
artifact — the server never needs Go installed.

Developer workflow for Go functions:
  flagbase fn init my-fn        # scaffold a local project
  cd my-fn && vim main.go       # write your code
  flagbase fn build             # compile to function.wasm (requires Go)
  flagbase fn deploy            # upload WASM to the server

To work on an existing function:
  flagbase fn pull <function-id>  # download scaffold wired to that function
  cd <function-id> && vim main.go
  flagbase fn build && flagbase fn deploy`,
}

var fnInitCmd = &cobra.Command{
	Use:   "init <name>",
	Short: "Scaffold a new Go function project locally",
	Args:  cobra.ExactArgs(1),
	RunE:  runFnInit,
}

var fnBuildCmd = &cobra.Command{
	Use:   "build [dir]",
	Short: "Compile the function to WASM (requires Go installed locally)",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runFnBuild,
}

var fnDeployCmd = &cobra.Command{
	Use:   "deploy [dir]",
	Short: "Upload compiled function.wasm to the Flagbase server",
	Long: `Upload function.wasm to Flagbase. If .flagbase.json contains a function_id the
previous function is deleted and replaced so the ID stays stable. Run
'flagbase fn build' first.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runFnDeploy,
}

var fnPullCmd = &cobra.Command{
	Use:   "pull <function-id> [target-dir]",
	Short: "Download a function scaffold from the server",
	Long: `Download a starter Go project from the server pre-wired to the given
function ID. After editing main.go, run 'flagbase fn build' then
'flagbase fn deploy' to re-upload the compiled WASM.`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runFnPull,
}

func init() {
	fnCmd.PersistentFlags().StringVar(&fnServer, "server", "",
		"Flagbase server URL (overrides FLAGBASE_SERVER; default http://localhost:8080)")
	fnCmd.PersistentFlags().StringVar(&fnToken, "token", "",
		"Auth token (overrides FLAGBASE_TOKEN env)")

	fnCmd.AddCommand(fnInitCmd, fnBuildCmd, fnDeployCmd, fnPullCmd)
	rootCmd.AddCommand(fnCmd)
}

// ---------- helpers ----------

func effectiveServer() string {
	if fnServer != "" {
		return strings.TrimRight(fnServer, "/")
	}
	if v := os.Getenv("FLAGBASE_SERVER"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://localhost:8080"
}

func effectiveToken() string {
	if fnToken != "" {
		return fnToken
	}
	return os.Getenv("FLAGBASE_TOKEN")
}

func readProjectConfig(dir string) (*fnProjectConfig, error) {
	data, err := os.ReadFile(filepath.Join(dir, ".flagbase.json"))
	if err != nil {
		return nil, fmt.Errorf("reading .flagbase.json: %w\n(run 'flagbase fn init <name>' to create a project)", err)
	}
	var cfg fnProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing .flagbase.json: %w", err)
	}
	return &cfg, nil
}

func writeProjectConfig(dir string, cfg *fnProjectConfig) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".flagbase.json"), append(b, '\n'), 0o644)
}

// ---------- init ----------

func runFnInit(_ *cobra.Command, args []string) error {
	name := args[0]
	dir := name

	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("directory %q already exists", dir)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	server := effectiveServer()

	files := map[string]struct {
		content string
		mode    os.FileMode
	}{
		"main.go": {content: fmt.Sprintf(`package main

import "fmt"

// main is the entry point for your Flagbase function.
// Write to stdout — the output is captured and returned to the caller.
func main() {
	fmt.Println("Hello from %s!")
}
`, name), mode: 0o644},

		"go.mod": {content: fmt.Sprintf("module flagbase-fn-%s\n\ngo 1.21\n",
			sanitizeName(name)), mode: 0o644},

		"build.sh": {content: `#!/bin/sh
set -e
GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -o function.wasm .
echo "Built function.wasm ($(wc -c < function.wasm) bytes)"
`, mode: 0o755},
	}

	for fname, f := range files {
		if err := os.WriteFile(filepath.Join(dir, fname), []byte(f.content), f.mode); err != nil {
			return fmt.Errorf("writing %s: %w", fname, err)
		}
	}

	cfg := &fnProjectConfig{Server: server, Name: name}
	if err := writeProjectConfig(dir, cfg); err != nil {
		return fmt.Errorf("writing .flagbase.json: %w", err)
	}

	fmt.Printf("Scaffolded function project in ./%s\n\n", dir)
	fmt.Printf("Next steps:\n")
	fmt.Printf("  cd %s\n", dir)
	fmt.Printf("  # edit main.go\n")
	fmt.Printf("  flagbase fn build    # compile to function.wasm\n")
	fmt.Printf("  flagbase fn deploy   # upload to %s\n", server)
	return nil
}

// ---------- build ----------

func runFnBuild(_ *cobra.Command, args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	outPath := filepath.Join(dir, "function.wasm")
	c := exec.Command("go", "build", "-o", outPath, ".") //nolint:gosec
	c.Dir = dir
	c.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "CGO_ENABLED=0")
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr

	fmt.Printf("Compiling %s → function.wasm ...\n", dir)
	if err := c.Run(); err != nil {
		return fmt.Errorf("build failed")
	}

	info, err := os.Stat(outPath)
	if err != nil {
		return fmt.Errorf("reading artifact: %w", err)
	}
	fmt.Printf("OK  function.wasm (%d bytes)\n", info.Size())
	return nil
}

// ---------- deploy ----------

func runFnDeploy(_ *cobra.Command, args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	cfg, err := readProjectConfig(dir)
	if err != nil {
		return err
	}

	server := effectiveServer()
	if cfg.Server != "" {
		server = strings.TrimRight(cfg.Server, "/")
	}

	token := effectiveToken()
	if token == "" {
		return fmt.Errorf("auth token required: set FLAGBASE_TOKEN env or use --token flag")
	}

	wasmPath := filepath.Join(dir, "function.wasm")
	wasmBytes, err := os.ReadFile(wasmPath)
	if err != nil {
		return fmt.Errorf("reading function.wasm: %w\n(run 'flagbase fn build' first)", err)
	}

	name := cfg.Name
	if name == "" {
		name = filepath.Base(dir)
		if name == "." {
			wd, _ := os.Getwd()
			name = filepath.Base(wd)
		}
	}

	// If a function_id is already recorded, delete the old one so the new
	// deployment takes its place without accumulating stale records.
	if cfg.FunctionID != "" {
		fmt.Printf("Replacing existing function %s ...\n", cfg.FunctionID)
		if delErr := deleteFunction(server, token, cfg.FunctionID); delErr != nil {
			fmt.Printf("Warning: could not delete previous function (%v); continuing.\n", delErr)
		}
	}

	fmt.Printf("Uploading %s to %s ...\n", wasmPath, server)
	fn, err := uploadWASM(server, token, name, "", wasmBytes)
	if err != nil {
		return err
	}

	cfg.FunctionID = fn.ID
	cfg.Server = server
	if err := writeProjectConfig(dir, cfg); err != nil {
		fmt.Printf("Warning: could not update .flagbase.json: %v\n", err)
	}

	fmt.Printf("Deployed  name=%s  id=%s  status=%s\n", fn.Name, fn.ID, fn.Status)
	return nil
}

type fnResponse struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

func uploadWASM(server, token, name, description string, wasmBytes []byte) (*fnResponse, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("name", name)
	_ = mw.WriteField("description", description)
	_ = mw.WriteField("language", "go")

	fw, err := mw.CreateFormFile("artifact", "function.wasm")
	if err != nil {
		return nil, fmt.Errorf("building upload: %w", err)
	}
	if _, err := fw.Write(wasmBytes); err != nil {
		return nil, fmt.Errorf("writing wasm: %w", err)
	}
	mw.Close()

	req, err := http.NewRequest(http.MethodPost, server+"/admin/api/functions", &body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("uploading: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var fn fnResponse
	if err := json.NewDecoder(resp.Body).Decode(&fn); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &fn, nil
}

func deleteFunction(server, token, id string) error {
	req, err := http.NewRequest(http.MethodDelete, server+"/admin/api/functions/"+id, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}

// ---------- pull ----------

func runFnPull(_ *cobra.Command, args []string) error {
	id := args[0]
	dir := id
	if len(args) > 1 {
		dir = args[1]
	}

	server := effectiveServer()
	token := effectiveToken()
	if token == "" {
		return fmt.Errorf("auth token required: set FLAGBASE_TOKEN env or use --token flag")
	}

	req, err := http.NewRequest(http.MethodGet, server+"/admin/api/functions/"+id+"/scaffold", nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading scaffold: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	zipBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response: %w", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		return fmt.Errorf("opening zip: %w", err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory %s: %w", dir, err)
	}

	for _, f := range zr.File {
		dest := filepath.Join(dir, f.Name) //nolint:gosec
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("opening %s: %w", f.Name, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("reading %s: %w", f.Name, err)
		}
		if err := os.WriteFile(dest, data, f.Mode()); err != nil {
			return fmt.Errorf("writing %s: %w", dest, err)
		}
	}

	fmt.Printf("Scaffold downloaded to ./%s\n\n", dir)
	fmt.Printf("Next steps:\n")
	fmt.Printf("  cd %s\n", dir)
	fmt.Printf("  # edit main.go\n")
	fmt.Printf("  flagbase fn build    # compile to function.wasm\n")
	fmt.Printf("  flagbase fn deploy   # upload updated WASM\n")
	return nil
}

// ---------- util ----------

func sanitizeName(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		if r >= 'A' && r <= 'Z' {
			return r + 32
		}
		return '-'
	}, name)
}
