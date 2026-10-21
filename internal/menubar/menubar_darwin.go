//go:build darwin

package menubar

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework Cocoa
#include <stdlib.h>
#include "menubar_darwin.h"
*/
import "C"

import (
	"fmt"
	"unsafe"
)

// Menu is the macOS status bar item.
type Menu struct {
	opts Options
}

// New puts an item in the menu bar.
//
// The item is a small wrench; clicking it opens a menu with the interface
// entry, the address of the running server and the quit entry.
func New(opts Options) (*Menu, error) {
	if opts.Title == "" {
		opts.Title = "打开 mymac 界面"
	}
	active = &Menu{opts: opts}

	title := C.CString(opts.Title)
	address := C.CString(opts.Address)
	defer C.free(unsafe.Pointer(title))
	defer C.free(unsafe.Pointer(address))

	if C.mymac_menubar_create(title, address) == 0 {
		active = nil
		return nil, fmt.Errorf("无法在菜单栏创建状态项")
	}
	return active, nil
}

// Run enters the Cocoa event loop.
//
// The loop has to run on the main thread, so the caller must lock the main
// goroutine to its thread before calling Run. It blocks until the user quits
// the app from the menu.
func (m *Menu) Run() {
	C.mymac_menubar_run()
}

// active is the menu the exported callbacks belong to. There is only ever
// one menu bar item per process.
var active *Menu

//export mymacMenuOpen
func mymacMenuOpen() {
	if active != nil && active.opts.OnOpen != nil {
		active.opts.OnOpen()
	}
}

//export mymacMenuQuit
func mymacMenuQuit() {
	if active != nil && active.opts.OnQuit != nil {
		active.opts.OnQuit()
	}
}
