//go:build !darwin

package menubar

import "errors"

// Menu is the stub used on platforms without a menu bar.
type Menu struct {
	opts Options
}

// New reports that the menu bar is not available on this platform.
func New(opts Options) (*Menu, error) {
	return nil, errors.New("菜单栏只在 macOS 上可用")
}

// Run returns immediately.
func (m *Menu) Run() {}
