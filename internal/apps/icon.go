package apps

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"mymac/internal/config"
)

// iconCache remembers the icons that were already converted.
var iconCache sync.Map // appPath -> iconResult

type iconResult struct {
	data []byte
	err  error
}

// Icon returns the PNG icon of an app bundle.
//
// The .icns file is converted with sips and cached under
// ~/.config/mymac/icons so the conversion happens only once.
func Icon(appPath string) ([]byte, error) {
	if v, ok := iconCache.Load(appPath); ok {
		r := v.(iconResult)
		return r.data, r.err
	}
	data, err := buildIcon(appPath)
	iconCache.Store(appPath, iconResult{data: data, err: err})
	return data, err
}

// buildIcon converts the app icon to PNG.
func buildIcon(appPath string) ([]byte, error) {
	icns := findIcns(appPath)
	if icns == "" {
		return nil, os.ErrNotExist
	}

	dir := config.IconsDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(icns))
	name := hex.EncodeToString(sum[:8]) + ".png"
	dst := filepath.Join(dir, name)

	if b, err := os.ReadFile(dst); err == nil {
		return b, nil
	}

	tmp := filepath.Join(dir, ".tmp-"+name)
	cmd := exec.Command("/usr/bin/sips", "-s", "format", "png", icns, "--out", tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		_ = os.Remove(tmp)
		return nil, &convertError{path: icns, out: string(out), err: err}
	}
	data, err := os.ReadFile(tmp)
	_ = os.Remove(tmp)
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return nil, err
	}
	return data, nil
}

// convertError explains a failed icon conversion.
type convertError struct {
	path string
	out  string
	err  error
}

func (e *convertError) Error() string {
	msg := strings.TrimSpace(e.out)
	if msg == "" {
		return "无法转换图标: " + e.err.Error()
	}
	return "无法转换图标 " + e.path + ": " + msg
}

// findIcns locates the icon file of an app bundle.
func findIcns(appPath string) string {
	resources := filepath.Join(appPath, "Contents", "Resources")
	entries, err := os.ReadDir(resources)
	if err != nil {
		return ""
	}
	// Prefer the icon named in Info.plist.
	plist, err := os.ReadFile(filepath.Join(appPath, "Contents", "Info.plist"))
	if err == nil {
		if doc, err := parsePlist(plist); err == nil {
			if name := plistString(doc, "CFBundleIconFile"); name != "" {
				for _, candidate := range []string{name, name + ".icns"} {
					p := filepath.Join(resources, candidate)
					if _, err := os.Stat(p); err == nil {
						return p
					}
				}
			}
		}
	}
	// Otherwise take the first .icns file.
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".icns") {
			return filepath.Join(resources, e.Name())
		}
	}
	return ""
}
