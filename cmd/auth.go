package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

type credentials struct {
	Token  string `json:"token"`
	Server string `json:"server"`
	Email  string `json:"email"`
}

var (
	authServer   string
	authEmail    string
	authPassword string
)

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage authentication credentials",
}

var authLoginCmd = &cobra.Command{
	Use:   "login",
	Short: "Authenticate with a Flagbase server and save your token locally",
	Long: `Log in to a Flagbase server and save the auth token to ~/.flagbase/credentials.json.

Once logged in, fn commands (deploy, pull) will use the saved token automatically —
no need to pass --token or set FLAGBASE_TOKEN every time.

Example:
  flagbase auth login
  flagbase auth login --server http://myserver:8080
  flagbase auth login --email admin@example.com --password secret`,
	RunE: runAuthLogin,
}

func init() {
	authLoginCmd.Flags().StringVar(&authServer, "server", "",
		"Flagbase server URL (overrides FLAGBASE_SERVER; default http://localhost:8080)")
	authLoginCmd.Flags().StringVar(&authEmail, "email", "", "Email address")
	authLoginCmd.Flags().StringVar(&authPassword, "password", "", "Password")

	authCmd.AddCommand(authLoginCmd)
	rootCmd.AddCommand(authCmd)
}

func credentialsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".flagbase", "credentials.json"), nil
}

func loadSavedCredentials() *credentials {
	path, err := credentialsPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var c credentials
	if err := json.Unmarshal(data, &c); err != nil {
		return nil
	}
	return &c
}

func saveCredentials(c *credentials) error {
	path, err := credentialsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating credentials directory: %w", err)
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func effectiveAuthServer() string {
	if authServer != "" {
		return strings.TrimRight(authServer, "/")
	}
	if v := os.Getenv("FLAGBASE_SERVER"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "http://localhost:8080"
}

func runAuthLogin(_ *cobra.Command, _ []string) error {
	scanner := bufio.NewScanner(os.Stdin)
	server := effectiveAuthServer()
	email := authEmail
	password := authPassword

	if email == "" {
		fmt.Printf("Logging in to %s\nEmail: ", server)
		scanner.Scan()
		email = strings.TrimSpace(scanner.Text())
	}
	if password == "" {
		fmt.Print("Password: ")
		scanner.Scan()
		password = scanner.Text()
	}

	if email == "" {
		return fmt.Errorf("email is required")
	}
	if password == "" {
		return fmt.Errorf("password is required")
	}

	body := fmt.Sprintf(`{"email":%q,"password":%q}`, email, password)
	req, err := http.NewRequest(http.MethodPost, server+"/auth/login", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", server, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}

	var result struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil || result.Token == "" {
		return fmt.Errorf("unexpected server response")
	}

	if err := saveCredentials(&credentials{Token: result.Token, Server: server, Email: email}); err != nil {
		return fmt.Errorf("saving credentials: %w", err)
	}

	fmt.Printf("\nLogged in as %s\nCredentials saved to ~/.flagbase/credentials.json\n\n", email)
	fmt.Printf("You can now run 'flagbase fn deploy' without --token.\n")
	return nil
}
