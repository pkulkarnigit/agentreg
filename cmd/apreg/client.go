package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"golang.org/x/term"
)

// stdinReader is shared across all prompts in a single command invocation.
// A fresh bufio.Reader per prompt would read ahead and buffer input meant
// for the *next* prompt, silently discarding it once that reader is
// dropped — this matters for piped/non-interactive input (tests, scripts).
var stdinReader = bufio.NewReader(os.Stdin)

func promptLine(label string) (string, error) {
	fmt.Print(label)
	line, err := stdinReader.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func promptPassword(label string) (string, error) {
	fmt.Print(label)
	if term.IsTerminal(int(os.Stdin.Fd())) {
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Println()
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	// Not a terminal (piped input, CI, etc.) — fall back to a plain read.
	return promptLine("")
}

// apiError mirrors the {"error": "..."} JSON body every apreg-server error
// response uses.
type apiError struct {
	Error string `json:"error"`
}

func decodeJSONBody(resp *http.Response, out any) error {
	return json.NewDecoder(resp.Body).Decode(out)
}

func doJSON(method, url, token string, body any, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reqBody = strings.NewReader(string(b))
	}
	req, err := http.NewRequest(method, url, reqBody)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		var ae apiError
		b, _ := io.ReadAll(resp.Body)
		if json.Unmarshal(b, &ae) == nil && ae.Error != "" {
			return fmt.Errorf("%s (HTTP %d)", ae.Error, resp.StatusCode)
		}
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}

	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// parseRef parses "@scope/name" or "@scope/name@version" into its parts.
// version is "" (meaning latest) if not given.
func parseRef(ref string) (scope, name, version string, err error) {
	if !strings.HasPrefix(ref, "@") {
		return "", "", "", fmt.Errorf("expected a reference like @scope/name or @scope/name@version, got %q", ref)
	}
	ref = strings.TrimPrefix(ref, "@")

	slash := strings.Index(ref, "/")
	if slash < 0 {
		return "", "", "", fmt.Errorf("expected @scope/name, got %q", "@"+ref)
	}
	scope = ref[:slash]
	rest := ref[slash+1:]

	if at := strings.Index(rest, "@"); at >= 0 {
		name = rest[:at]
		version = rest[at+1:]
	} else {
		name = rest
	}
	if scope == "" || name == "" {
		return "", "", "", fmt.Errorf("expected @scope/name, got %q", "@"+ref)
	}
	return scope, name, version, nil
}
