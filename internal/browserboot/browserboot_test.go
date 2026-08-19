package browserboot

import (
	"path/filepath"
	"testing"
)

func TestNewestManagedCloak(t *testing.T) {
	paths := []string{
		filepath.Join(`C:\Users\Admin\.cloakbrowser`, `chromium-146.0.7680.177.5`, `chrome.exe`),
		filepath.Join(`C:\Users\Admin\.cloakbrowser`, `chromium-151.0.7922.108.2-pro`, `chrome.exe`),
	}
	want := paths[1]
	if got := newestManagedCloak(paths); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestManagedCloakVersionSupportsProSuffix(t *testing.T) {
	path := filepath.Join(`C:\Users\Admin\.cloakbrowser`, `chromium-151.0.7922.108.2-pro`, `chrome.exe`)
	version, ok := managedCloakVersion(path)
	if !ok || len(version) != 5 || version[0] != 151 || version[4] != 2 {
		t.Fatalf("version=%v ok=%v", version, ok)
	}
}
