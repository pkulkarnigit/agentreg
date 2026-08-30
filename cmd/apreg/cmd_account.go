package main

import (
	"flag"
	"fmt"
)

func cmdSignup(args []string) error {
	fs := flag.NewFlagSet("signup", flag.ExitOnError)
	registry := fs.String("registry", "", "registry URL, e.g. http://localhost:8080")
	fs.Parse(args)
	if *registry == "" {
		return fmt.Errorf("--registry is required")
	}

	username, err := promptLine("Username: ")
	if err != nil {
		return err
	}
	email, err := promptLine("Email: ")
	if err != nil {
		return err
	}
	password, err := promptPassword("Password: ")
	if err != nil {
		return err
	}

	var resp struct {
		Username string `json:"username"`
	}
	if err := doJSON("POST", *registry+"/v1/users", "", map[string]string{
		"username": username, "email": email, "password": password,
	}, &resp); err != nil {
		return fmt.Errorf("signup failed: %w", err)
	}

	fmt.Printf("Account %q created. Run `apreg login --registry %s` to authenticate.\n", resp.Username, *registry)
	return nil
}

func cmdLogin(args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	registry := fs.String("registry", "", "registry URL, e.g. http://localhost:8080")
	fs.Parse(args)
	if *registry == "" {
		return fmt.Errorf("--registry is required")
	}

	username, err := promptLine("Username: ")
	if err != nil {
		return err
	}
	password, err := promptPassword("Password: ")
	if err != nil {
		return err
	}

	var resp struct {
		Token    string `json:"token"`
		Username string `json:"username"`
	}
	if err := doJSON("POST", *registry+"/v1/tokens", "", map[string]string{
		"username": username, "password": password, "label": "cli",
	}, &resp); err != nil {
		return fmt.Errorf("login failed: %w", err)
	}

	if err := saveConfig(&cliConfig{Registry: *registry, Username: resp.Username, Token: resp.Token}); err != nil {
		return fmt.Errorf("save config: %w", err)
	}

	fmt.Printf("Logged in as %q on %s\n", resp.Username, *registry)
	return nil
}
