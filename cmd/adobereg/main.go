package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"chatgpt-register/internal/adobereg"
)

type profile struct {
	Email     string
	FirstName string
	LastName  string
}

func main() {
	emails := flag.String("emails", "", "comma-separated email addresses")
	names := flag.String("names", "", "comma-separated First:Last pairs")
	country := flag.String("country", "", "two-letter country code; empty keeps page default")
	birthYear := flag.Int("birth-year", 1994, "birth year")
	birthMonth := flag.Int("birth-month", 6, "birth month, 1-12")
	proxy := flag.String("proxy", "", "browser proxy URL")
	browserBin := flag.String("browser-bin", os.Getenv("CHROME_BIN"), "Chrome/Chromium executable")
	cloak := flag.Bool("cloak", false, "launch the browser binary in CloakBrowser mode")
	headless := flag.Bool("headless", false, "run browser without a visible window")
	submit := flag.Bool("submit", false, "submit the final create-account action")
	outDir := flag.String("out", "adobe-output", "result and screenshot directory")
	flag.Parse()

	profiles, err := parseProfiles(*emails, *names)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := os.MkdirAll(*outDir, 0700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	for _, p := range profiles {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		result, regErr := adobereg.Register(ctx, adobereg.Input{
			Email: p.Email, FirstName: p.FirstName, LastName: p.LastName,
			BirthYear: *birthYear, BirthMonth: *birthMonth, Country: *country,
			Proxy: *proxy, BrowserBin: *browserBin, CloakBrowser: *cloak,
			Headless: *headless, DryRun: !*submit,
			Log: func(format string, args ...any) {
				fmt.Printf("[%s] %s\n", p.Email, fmt.Sprintf(format, args...))
			},
			SaveShot: func(png []byte) {
				_ = os.WriteFile(filepath.Join(*outDir, safeName(p.Email)+".png"), png, 0600)
			},
		})
		cancel()
		if regErr != nil {
			fmt.Fprintf(os.Stderr, "[%s] %v\n", p.Email, regErr)
			continue
		}
		data, _ := json.MarshalIndent(result, "", "  ")
		path := filepath.Join(*outDir, safeName(p.Email)+".json")
		if err := os.WriteFile(path, data, 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
			continue
		}
		fmt.Printf("[%s] %s -> %s\n", p.Email, result.Status, path)
	}
}

func parseProfiles(rawEmails, rawNames string) ([]profile, error) {
	emailList := splitCSV(rawEmails)
	if len(emailList) == 0 {
		return nil, fmt.Errorf("-emails is required")
	}
	nameList := splitCSV(rawNames)
	if len(nameList) > 0 && len(nameList) != len(emailList) {
		return nil, fmt.Errorf("-names count must match -emails count")
	}
	out := make([]profile, 0, len(emailList))
	for i, email := range emailList {
		p := profile{Email: email}
		if len(nameList) > 0 {
			parts := strings.SplitN(nameList[i], ":", 2)
			if len(parts) != 2 {
				return nil, fmt.Errorf("name %s must use First:Last", strconv.Quote(nameList[i]))
			}
			p.FirstName, p.LastName = strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		}
		out = append(out, p)
	}
	return out, nil
}

func splitCSV(s string) []string {
	var out []string
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func safeName(s string) string {
	r := strings.NewReplacer("@", "_at_", ".", "_", "/", "_", "\\", "_")
	return r.Replace(s)
}
