package cmd

import (
	"archive/zip"
	"bufio"
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

	"github.com/mtfuller/flagbase/internal/scaffold"

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

var fnRunCmd = &cobra.Command{
	Use:   "run [dir]",
	Short: "Build, deploy, and invoke a function in one step",
	Long: `Compile the function to WASM, upload it to the server, invoke it, and
stream the output to stdout. Equivalent to running fn build + fn deploy +
fn invoke in sequence. Useful for rapid iteration.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runFnRun,
}

func init() {
	fnCmd.PersistentFlags().StringVar(&fnServer, "server", "",
		"Flagbase server URL (overrides FLAGBASE_SERVER; default http://localhost:8080)")
	fnCmd.PersistentFlags().StringVar(&fnToken, "token", "",
		"Auth token (overrides FLAGBASE_TOKEN env)")

	fnCmd.AddCommand(fnInitCmd, fnBuildCmd, fnDeployCmd, fnPullCmd, fnRunCmd)
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
	if c := loadSavedCredentials(); c != nil && c.Server != "" {
		return strings.TrimRight(c.Server, "/")
	}
	return "http://localhost:8080"
}

func effectiveToken() string {
	if fnToken != "" {
		return fnToken
	}
	if v := os.Getenv("FLAGBASE_TOKEN"); v != "" {
		return v
	}
	if c := loadSavedCredentials(); c != nil && c.Token != "" {
		return c.Token
	}
	return ""
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
	safeName := scaffold.SafeName(name)

	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("directory %q already exists", dir)
	}
	if err := os.MkdirAll(filepath.Join(dir, "fnruntime"), 0o755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	server := effectiveServer()

	type entry struct {
		data []byte
		mode os.FileMode
	}
	files := map[string]entry{
		"main.go":      {scaffold.MainGo(name, safeName), 0o644},
		"main_test.go": {scaffold.TestMainGo(safeName), 0o644},
		"go.mod":       {scaffold.GoMod(name), 0o644},
		"build.sh":     {[]byte(scaffold.BuildSh), 0o755},

		"fnruntime/doc.go":            {[]byte(scaffold.FnruntimeDocGo), 0o644},
		"fnruntime/runtime_wasip1.go": {[]byte(scaffold.FnruntimeRuntimeWasip1Go), 0o644},
		"fnruntime/bucket_wasip1.go":  {[]byte(scaffold.FnruntimeBucketWasip1Go), 0o644},
		"fnruntime/flags_wasip1.go":   {[]byte(scaffold.FnruntimeFlagsWasip1Go), 0o644},
		"fnruntime/invoke_wasip1.go":  {[]byte(scaffold.FnruntimeInvokeWasip1Go), 0o644},
		"fnruntime/fetch_wasip1.go":   {[]byte(scaffold.FnruntimeFetchWasip1Go), 0o644},
		"fnruntime/table_wasip1.go":   {[]byte(scaffold.FnruntimeTableWasip1Go), 0o644},
		"fnruntime/host.go":           {[]byte(scaffold.FnruntimeHostGo), 0o644},
		"fnruntime/mock.go":           {[]byte(scaffold.FnruntimeMockGo), 0o644},
	}

	for fname, f := range files {
		if err := os.WriteFile(filepath.Join(dir, fname), f.data, f.mode); err != nil {
			return fmt.Errorf("writing %s: %w", fname, err)
		}
	}

	cfg := &fnProjectConfig{Server: server, Name: name}
	if err := writeProjectConfig(dir, cfg); err != nil {
		return fmt.Errorf("writing .flagbase.json: %w", err)
	}

	fmt.Printf("Scaffolded function project in ./%s\n\n", dir)
	fmt.Printf("Next steps:\n")
	fmt.Printf("  flagbase auth login            # log in once and save your token\n")
	fmt.Printf("  cd %s\n", dir)
	fmt.Printf("  go test ./...                  # run unit tests (no server needed)\n")
	fmt.Printf("  flagbase fn run                # build, deploy, and invoke on %s\n", server)
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
		return fmt.Errorf("auth token required: run 'flagbase auth login' or set --token / FLAGBASE_TOKEN")
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

	// If a function_id is recorded, deploy a new version on the existing function.
	if cfg.FunctionID != "" {
		fmt.Printf("Deploying new version for function %s ...\n", cfg.FunctionID)
		version, err := deployVersion(server, token, cfg.FunctionID, wasmBytes)
		if err == nil {
			cfg.Server = server
			if werr := writeProjectConfig(dir, cfg); werr != nil {
				fmt.Printf("Warning: could not update .flagbase.json: %v\n", werr)
			}
			fmt.Printf("Deployed  name=%s  id=%s  version=v%d\n", name, cfg.FunctionID, version)
			return nil
		}
		fmt.Printf("Warning: could not update existing function (%v); creating new.\n", err)
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

type fnVersionResponse struct {
	ID         string `json:"id"`
	FunctionID string `json:"function_id"`
	Version    int    `json:"version"`
}

func deployVersion(server, token, id string, wasmBytes []byte) (int, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("artifact", "function.wasm")
	if err != nil {
		return 0, fmt.Errorf("building upload: %w", err)
	}
	if _, err := fw.Write(wasmBytes); err != nil {
		return 0, fmt.Errorf("writing wasm: %w", err)
	}
	mw.Close()

	req, err := http.NewRequest(http.MethodPost, server+"/admin/api/functions/"+id+"/versions", &body)
	if err != nil {
		return 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("uploading: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var v fnVersionResponse
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return 1, nil
	}
	return v.Version, nil
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
		return fmt.Errorf("auth token required: run 'flagbase auth login' or set --token / FLAGBASE_TOKEN")
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
	fmt.Printf("  # edit main.go, then:\n")
	fmt.Printf("  flagbase fn build              # compile to function.wasm\n")
	fmt.Printf("  flagbase fn deploy --token <token>  # upload updated WASM\n")
	return nil
}

// ---------- run ----------

func runFnRun(cmd *cobra.Command, args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	// build
	if err := runFnBuild(cmd, []string{dir}); err != nil {
		return err
	}

	// deploy (creates or updates, stores function_id in .flagbase.json)
	if err := runFnDeploy(cmd, []string{dir}); err != nil {
		return err
	}

	// read config to get the function ID written by deploy
	cfg, err := readProjectConfig(dir)
	if err != nil {
		return err
	}
	if cfg.FunctionID == "" {
		return fmt.Errorf("deploy did not record a function_id in .flagbase.json")
	}

	server := effectiveServer()
	if cfg.Server != "" {
		server = strings.TrimRight(cfg.Server, "/")
	}
	token := effectiveToken()
	if token == "" {
		return fmt.Errorf("auth token required: run 'flagbase auth login' or set --token / FLAGBASE_TOKEN")
	}

	fmt.Printf("Invoking %s ...\n", cfg.FunctionID)
	return streamInvoke(server, token, cfg.FunctionID)
}

// streamInvoke connects to the SSE invoke/stream endpoint and writes stdout
// chunks to os.Stdout as they arrive.
func streamInvoke(server, token, id string) error {
	req, err := http.NewRequest(http.MethodGet,
		server+"/admin/api/functions/"+id+"/invoke/stream", nil)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("invoking function: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	// Parse SSE: each event is "data: <json>\n\n"
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimPrefix(line, "data: ")

		var event struct {
			Type    string `json:"type"`
			Data    string `json:"data"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		switch event.Type {
		case "stdout":
			fmt.Print(event.Data)
		case "done":
			return nil
		case "error":
			return fmt.Errorf("function error: %s", event.Message)
		}
	}
	return scanner.Err()
}
