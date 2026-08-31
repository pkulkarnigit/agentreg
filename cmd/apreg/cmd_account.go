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

func cmdVerifyEmail(args []string) error {
	fs := flag.NewFlagSet("verify-email", flag.ExitOnError)
	registryFlag := fs.String("registry", "", "registry URL (defaults to the one from `apreg login`)")
	fs.Parse(args)
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: apreg verify-email <token>")
	}

	base := *registryFlag
	if base == "" {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		base = cfg.Registry
	}
	if base == "" {
		return fmt.Errorf("no registry configured — pass --registry <url> or run `apreg login` first")
	}

	if err := doJSON("POST", base+"/v1/users/verify", "", map[string]string{"token": fs.Arg(0)}, nil); err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}
	fmt.Println("Email verified.")
	return nil
}

func cmdResetPassword(args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	registryFlag := fs.String("registry", "", "registry URL (defaults to the one from `apreg login`)")
	tokenFlag := fs.String("token", "", "reset token from the email/log — skips the request step and prompts only for the new password")
	fs.Parse(args)

	base := *registryFlag
	if base == "" {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		base = cfg.Registry
	}
	if base == "" {
		return fmt.Errorf("no registry configured — pass --registry <url> or run `apreg login` first")
	}

	token := *tokenFlag
	if token == "" {
		username, err := promptLine("Username: ")
		if err != nil {
			return err
		}
		if err := doJSON("POST", base+"/v1/password-reset/request", "", map[string]string{"username": username}, nil); err != nil {
			return fmt.Errorf("reset request failed: %w", err)
		}
		fmt.Println("If that account exists, a reset link/token has been sent to its registered email (or logged server-side, if no mail provider is configured).")

		token, err = promptLine("Reset token: ")
		if err != nil {
			return err
		}
	}

	newPassword, err := promptPassword("New password: ")
	if err != nil {
		return err
	}

	if err := doJSON("POST", base+"/v1/password-reset/confirm", "", map[string]string{
		"token": token, "new_password": newPassword,
	}, nil); err != nil {
		return fmt.Errorf("reset failed: %w", err)
	}

	fmt.Println("Password reset. Run `apreg login` to authenticate with it.")
	return nil
}
