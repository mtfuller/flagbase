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
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// feProjectConfig mirrors .flagbase-frontend.json written into every scaffolded project.
type feProjectConfig struct {
	Server     string `json:"server"`
	FrontendID string `json:"frontend_id"`
	Name       string `json:"name"`
}

var feCmd = &cobra.Command{
	Use:   "fe",
	Short: "Manage hosted frontends",
	Long: `Create and deploy static frontends to Flagbase.

Each frontend is served under /frontends/<slug>/ using its active version.
Upload ZIP archives as versioned deployments and activate the one to serve.

Typical workflow:
  flagbase auth login                       # authenticate once
  flagbase fe create "My App" my-app        # create a frontend record
  flagbase fe init my-app --frontend-id <id> # scaffold a local project
  cd my-app && edit index.html
  flagbase fe deploy --label v1             # zip and upload to the server
  flagbase fe activate <frontend-id> <version-id>`,
}

var feInitCmd = &cobra.Command{
	Use:   "init <name>",
	Short: "Scaffold a local frontend project",
	Args:  cobra.ExactArgs(1),
	RunE:  runFeInit,
}

var feCreateCmd = &cobra.Command{
	Use:   "create <name> <slug>",
	Short: "Create a frontend record on the server",
	Args:  cobra.ExactArgs(2),
	RunE:  runFeCreate,
}

var feListCmd = &cobra.Command{
	Use:   "list",
	Short: "List frontends on the server",
	Args:  cobra.NoArgs,
	RunE:  runFeList,
}

var feDeployCmd = &cobra.Command{
	Use:   "deploy [dir]",
	Short: "Zip a directory and upload it as a new frontend version",
	Long: `Zip all files in [dir] (defaults to current directory) and upload them as a
new version of the target frontend. The frontend ID is read from
.flagbase-frontend.json in the project directory or supplied via --frontend-id.

Run 'flagbase fe activate' to make the uploaded version live.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runFeDeploy,
}

var feActivateCmd = &cobra.Command{
	Use:   "activate <frontend-id> <version-id>",
	Short: "Activate a version so it is served at /frontends/<slug>/",
	Args:  cobra.ExactArgs(2),
	RunE:  runFeActivate,
}

var (
	feDescription string
	feLabel       string
	feVersionDesc string
	feFrontendID  string
)

func init() {
	feCreateCmd.Flags().StringVar(&feDescription, "description", "", "Frontend description")
	feDeployCmd.Flags().StringVar(&feLabel, "label", "", "Version label, e.g. v1.0.0 (required)")
	feDeployCmd.Flags().StringVar(&feVersionDesc, "description", "", "Version description")
	feDeployCmd.Flags().StringVar(&feFrontendID, "frontend-id", "", "Frontend ID (overrides .flagbase-frontend.json)")
	feInitCmd.Flags().StringVar(&feFrontendID, "frontend-id", "", "Wire the project to an existing frontend ID")

	feCmd.AddCommand(feInitCmd, feCreateCmd, feListCmd, feDeployCmd, feActivateCmd)
	rootCmd.AddCommand(feCmd)
}

// ---------- config helpers ----------

func feConfigPath(dir string) string {
	return filepath.Join(dir, ".flagbase-frontend.json")
}

func readFeConfig(dir string) (*feProjectConfig, error) {
	data, err := os.ReadFile(feConfigPath(dir))
	if err != nil {
		return nil, fmt.Errorf("reading .flagbase-frontend.json: %w\n(run 'flagbase fe init <name>' to create a project)", err)
	}
	var cfg feProjectConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing .flagbase-frontend.json: %w", err)
	}
	return &cfg, nil
}

func writeFeConfig(dir string, cfg *feProjectConfig) error {
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(feConfigPath(dir), append(b, '\n'), 0o644)
}

// ---------- init ----------

func runFeInit(_ *cobra.Command, args []string) error {
	name := args[0]
	dir := name

	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("directory %q already exists", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating directory: %w", err)
	}

	indexHTML := "<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n" +
		"  <meta charset=\"UTF-8\">\n" +
		"  <meta name=\"viewport\" content=\"width=device-width, initial-scale=1.0\">\n" +
		"  <title>" + name + "</title>\n" +
		"  <style>\n" +
		"    body { font-family: system-ui, sans-serif; display: flex; align-items: center; justify-content: center; min-height: 100vh; margin: 0; background: #0f0f0f; color: #e2e8f0; }\n" +
		"    .card { text-align: center; padding: 40px; }\n" +
		"    h1 { font-size: 2rem; margin-bottom: 8px; }\n" +
		"    p { color: #94a3b8; }\n" +
		"    code { background: rgba(255,255,255,.08); padding: 2px 6px; border-radius: 4px; }\n" +
		"  </style>\n" +
		"</head>\n<body>\n" +
		"  <div class=\"card\">\n" +
		"    <h1>" + name + "</h1>\n" +
		"    <p>Edit <code>index.html</code> and deploy with <code>flagbase fe deploy --label v1</code></p>\n" +
		"  </div>\n" +
		"</body>\n</html>\n"

	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte(indexHTML), 0o644); err != nil {
		return fmt.Errorf("writing index.html: %w", err)
	}

	server := effectiveServer()
	cfg := &feProjectConfig{Server: server, Name: name, FrontendID: feFrontendID}
	if err := writeFeConfig(dir, cfg); err != nil {
		return fmt.Errorf("writing .flagbase-frontend.json: %w", err)
	}

	fmt.Printf("Scaffolded frontend project in ./%s\n\n", dir)
	fmt.Printf("Next steps:\n")
	if feFrontendID == "" {
		fmt.Printf("  flagbase auth login                             # log in once and save your token\n")
		slug := feSlugify(name)
		fmt.Printf("  flagbase fe create \"%s\" %s  # create a record on the server\n", name, slug)
	}
	fmt.Printf("  cd %s && edit index.html\n", dir)
	fmt.Printf("  flagbase fe deploy --label v1                   # zip and upload to %s\n", server)
	return nil
}

// feSlugify converts a name to a URL-safe slug.
func feSlugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		case c == '-', c == ' ', c == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// ---------- create ----------

type feResponse struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

func runFeCreate(_ *cobra.Command, args []string) error {
	name, slug := args[0], args[1]
	server := effectiveServer()
	token := effectiveToken()
	if token == "" {
		return fmt.Errorf("auth token required: run 'flagbase auth login' or set --token / FLAGBASE_TOKEN")
	}

	body, _ := json.Marshal(map[string]string{"name": name, "slug": slug, "description": feDescription})
	r, err := http.NewRequest(http.MethodPost, server+"/api/v1/frontends/", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", server, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var fe feResponse
	if err := json.NewDecoder(resp.Body).Decode(&fe); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}
	fmt.Printf("Created  name=%s  slug=%s  id=%s\n", fe.Name, fe.Slug, fe.ID)
	fmt.Printf("\nTo wire a local project to this frontend:\n")
	fmt.Printf("  flagbase fe init my-site --frontend-id %s\n", fe.ID)
	return nil
}

// ---------- list ----------

func runFeList(_ *cobra.Command, _ []string) error {
	server := effectiveServer()
	token := effectiveToken()
	if token == "" {
		return fmt.Errorf("auth token required: run 'flagbase auth login' or set --token / FLAGBASE_TOKEN")
	}

	r, err := http.NewRequest(http.MethodGet, server+"/api/v1/frontends/", nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	r.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", server, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var items []struct {
		ID              string `json:"id"`
		Name            string `json:"name"`
		Slug            string `json:"slug"`
		ActiveVersionID string `json:"active_version_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return fmt.Errorf("decoding response: %w", err)
	}

	if len(items) == 0 {
		fmt.Println("No frontends found.")
		return nil
	}
	fmt.Printf("%-36s  %-20s  %-20s  %s\n", "ID", "Name", "Slug", "Active")
	fmt.Println(strings.Repeat("-", 85))
	for _, f := range items {
		active := "no"
		if f.ActiveVersionID != "" {
			active = "yes"
		}
		fmt.Printf("%-36s  %-20s  %-20s  %s\n", f.ID, f.Name, f.Slug, active)
	}
	return nil
}

// ---------- deploy ----------

func runFeDeploy(_ *cobra.Command, args []string) error {
	dir := "."
	if len(args) > 0 {
		dir = args[0]
	}

	cfg, cfgErr := readFeConfig(dir)
	if cfg == nil {
		cfg = &feProjectConfig{}
	}

	frontendID := feFrontendID
	if frontendID == "" {
		frontendID = cfg.FrontendID
	}
	if frontendID == "" {
		if cfgErr != nil {
			return cfgErr
		}
		return fmt.Errorf("frontend ID required: use --frontend-id or run 'flagbase fe create' first")
	}

	if feLabel == "" {
		return fmt.Errorf("--label is required (e.g. --label v1.0.0)")
	}

	server := effectiveServer()
	if cfg.Server != "" {
		server = strings.TrimRight(cfg.Server, "/")
	}
	token := effectiveToken()
	if token == "" {
		return fmt.Errorf("auth token required: run 'flagbase auth login' or set --token / FLAGBASE_TOKEN")
	}

	fmt.Printf("Zipping %s ...\n", dir)
	zipBytes, fileCount, err := feZipDir(dir)
	if err != nil {
		return fmt.Errorf("zipping directory: %w", err)
	}
	fmt.Printf("OK  %d files, %d bytes\n", fileCount, len(zipBytes))

	fmt.Printf("Uploading to %s ...\n", server)
	version, err := feUploadVersion(server, token, frontendID, feLabel, feVersionDesc, zipBytes)
	if err != nil {
		return err
	}

	cfg.FrontendID = frontendID
	cfg.Server = server
	if werr := writeFeConfig(dir, cfg); werr != nil {
		fmt.Printf("Warning: could not update .flagbase-frontend.json: %v\n", werr)
	}

	fmt.Printf("Deployed  id=%s  label=%s  files=%d\n", version.ID, feLabel, version.FileCount)
	fmt.Printf("\nTo make this version live:\n")
	fmt.Printf("  flagbase fe activate %s %s\n", frontendID, version.ID)
	return nil
}

// feZipDir creates an in-memory ZIP of all files in dir, skipping hidden files and .flagbase-frontend.json.
func feZipDir(dir string) ([]byte, int, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, 0, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	fileCount := 0
	err = filepath.Walk(abs, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(abs, path)
		if err != nil {
			return err
		}
		// Skip config file and hidden files/dirs.
		if rel == ".flagbase-frontend.json" {
			return nil
		}
		for _, part := range strings.Split(rel, string(filepath.Separator)) {
			if strings.HasPrefix(part, ".") {
				return nil
			}
		}
		fw, err := zw.Create(filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.Copy(fw, f); err != nil {
			return err
		}
		fileCount++
		return nil
	})
	if err != nil {
		return nil, 0, err
	}
	if err := zw.Close(); err != nil {
		return nil, 0, err
	}
	if fileCount == 0 {
		return nil, 0, fmt.Errorf("no files found in %s", dir)
	}
	return buf.Bytes(), fileCount, nil
}

type feVersionResponse struct {
	ID        string `json:"id"`
	Label     string `json:"label"`
	FileCount int    `json:"file_count"`
}

func feUploadVersion(server, token, frontendID, label, description string, zipBytes []byte) (*feVersionResponse, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("label", label)
	_ = mw.WriteField("description", description)
	fw, err := mw.CreateFormFile("file", "frontend.zip")
	if err != nil {
		return nil, fmt.Errorf("building upload: %w", err)
	}
	if _, err := fw.Write(zipBytes); err != nil {
		return nil, fmt.Errorf("writing zip: %w", err)
	}
	mw.Close()

	r, err := http.NewRequest(http.MethodPost, server+"/api/v1/frontends/"+frontendID+"/versions", &body)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return nil, fmt.Errorf("uploading: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	var v feVersionResponse
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("decoding response: %w", err)
	}
	return &v, nil
}

// ---------- activate ----------

func runFeActivate(_ *cobra.Command, args []string) error {
	frontendID, versionID := args[0], args[1]
	server := effectiveServer()
	token := effectiveToken()
	if token == "" {
		return fmt.Errorf("auth token required: run 'flagbase auth login' or set --token / FLAGBASE_TOKEN")
	}

	r, err := http.NewRequest(http.MethodPut,
		server+"/api/v1/frontends/"+frontendID+"/versions/"+versionID+"/activate", nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	r.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(r)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", server, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("server returned %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}

	fmt.Printf("Activated  frontend=%s  version=%s\n", frontendID, versionID)
	return nil
}
