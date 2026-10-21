// Package menubar puts a small menu in the macOS menu bar.
//
// The menu is the way to reach a running mymac: it opens the web interface in
// the default browser and quits the process. On other platforms the package
// is a stub that reports the menu bar as unavailable.
package menubar

// Options configures the menu.
type Options struct {
	// Title labels the item that opens the interface.
	Title string
	// Address is the local URL of the web interface. It is shown in the
	// menu and opened by the interface item.
	Address string
	// OnOpen runs when the user picks the interface item.
	OnOpen func()
	// OnQuit runs when the user picks the quit item. It runs on the main
	// thread, so it must not block for long.
	OnQuit func()
}
