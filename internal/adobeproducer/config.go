package adobeproducer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"chatgpt-register/internal/browserboot"
	"chatgpt-register/internal/proxyutil"
)

func (p *Producer) configureStartOptions(opts *StartOptions) error {
	if rawMax := strings.TrimSpace(os.Getenv("ADOBE_MAX_CONCURRENCY")); rawMax != "" {
		if maxConcurrency, err := strconv.Atoi(rawMax); err == nil && maxConcurrency > 0 && opts.Concurrency > maxConcurrency {
			opts.Concurrency = maxConcurrency
		}
	}
	if headless := strings.TrimSpace(os.Getenv("ADOBE_HEADLESS")); headless != "" {
		opts.Headless = headless == "1" || strings.EqualFold(headless, "true")
	} else if runtime.GOOS != "windows" && strings.TrimSpace(os.Getenv("DISPLAY")) == "" {
		opts.Headless = true
	}
	if strings.TrimSpace(opts.BrowserMode) == "" {
		opts.BrowserMode = strings.TrimSpace(os.Getenv("ADOBE_BROWSER_MODE"))
	}
	if opts.BrowserMode == "" {
		opts.BrowserMode = strings.TrimSpace(p.setting("adobe_browser_mode"))
	}
	if opts.BrowserMode == "" {
		opts.BrowserMode = "cloak"
	}
	if opts.BrowserMode != "system" && opts.BrowserMode != "cloak" {
		return fmt.Errorf("Adobe 浏览器模式不正确")
	}
	if opts.BrowserMode == "cloak" {
		// The default CloakBrowser free license provides one active seat. Keep
		// producer scheduling serial so a UI value cannot make later launches
		// evict or reject the active browser.
		if opts.Concurrency > 1 {
			opts.Concurrency = 1
		}
		envBrowserPath := strings.TrimSpace(os.Getenv("CLOAK_BROWSER_PATH"))
		if strings.TrimSpace(opts.BrowserPath) == "" {
			opts.BrowserPath = envBrowserPath
		}
		if strings.TrimSpace(opts.BrowserPath) == "" {
			opts.BrowserPath = strings.TrimSpace(p.setting("adobe_cloak_browser_path"))
		}
		if strings.TrimSpace(opts.BrowserPath) == "" {
			opts.BrowserPath = discoverCloakBrowser()
		} else if envBrowserPath == "" {
			// A wrapper update installs a new versioned cache directory. Prefer it
			// over an older managed-cache path saved in the database/UI.
			if discovered := discoverCloakBrowser(); newerManagedCloak(discovered, opts.BrowserPath) {
				opts.BrowserPath = discovered
			}
		}
		if info, err := os.Stat(opts.BrowserPath); err != nil || info.IsDir() {
			path, downloadErr := browserboot.EnsureCloakBrowser(context.Background())
			if downloadErr != nil {
				return downloadErr
			}
			opts.BrowserPath = path
		}
	}

	proxyEnabledSetting := strings.TrimSpace(p.setting("proxy_enabled"))
	proxyEnabled := proxyEnabledSetting == "1"
	if envEnabled := strings.TrimSpace(os.Getenv("ADOBE_PROXY_ENABLED")); envEnabled != "" {
		proxyEnabled = envEnabled == "1" || strings.EqualFold(envEnabled, "true")
	}
	proxyRaw := strings.TrimSpace(os.Getenv("ADOBE_PROXY_LIST"))
	if proxyRaw == "" {
		proxyRaw = strings.TrimSpace(p.setting("proxy_list"))
	} else if proxyEnabledSetting == "" && strings.TrimSpace(os.Getenv("ADOBE_PROXY_ENABLED")) == "" {
		proxyEnabled = true
	}
	if proxyEnabled {
		opts.Proxies = proxyutil.List(proxyRaw)
		if len(opts.Proxies) == 0 {
			return fmt.Errorf("代理已启用，但代理列表为空")
		}
		for index, item := range opts.Proxies {
			if _, err := proxyutil.Parse(item); err != nil {
				return fmt.Errorf("第 %d 个代理格式错误: %w", index+1, err)
			}
		}
	}
	return nil
}

func newerManagedCloak(candidate, current string) bool {
	candidateVersion, candidateOK := managedCloakVersion(candidate)
	currentVersion, currentOK := managedCloakVersion(current)
	if !candidateOK || !currentOK {
		return false
	}
	length := len(candidateVersion)
	if len(currentVersion) > length {
		length = len(currentVersion)
	}
	for index := 0; index < length; index++ {
		candidatePart, currentPart := 0, 0
		if index < len(candidateVersion) {
			candidatePart = candidateVersion[index]
		}
		if index < len(currentVersion) {
			currentPart = currentVersion[index]
		}
		if candidatePart != currentPart {
			return candidatePart > currentPart
		}
	}
	return false
}

func managedCloakVersion(path string) ([]int, bool) {
	directory := filepath.Base(filepath.Dir(filepath.Clean(path)))
	if !strings.HasPrefix(strings.ToLower(directory), "chromium-") {
		return nil, false
	}
	raw := strings.TrimPrefix(strings.ToLower(directory), "chromium-")
	raw = strings.TrimSuffix(raw, "-pro")
	parts := strings.Split(raw, ".")
	version := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			return nil, false
		}
		version = append(version, value)
	}
	return version, len(version) > 0
}

func discoverCloakBrowser() string {
	return browserboot.DiscoverCloakBrowser()
}
