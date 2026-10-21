// Command mymac is a local macOS housekeeping tool: app uninstaller, system
// cleaner, process manager and environment variable manager behind a small
// local web UI.
//
// The process runs in the macOS menu bar: the menu opens the interface and
// quits the app. Only one instance may run at a time, and it always listens
// on the same port so the address can be typed by hand.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
	"time"

	"mymac/internal/apps"
	"mymac/internal/audit"
	"mymac/internal/config"
	"mymac/internal/envs"
	"mymac/internal/instance"
	"mymac/internal/menubar"
	"mymac/internal/procs"
	"mymac/internal/server"
	"mymac/internal/sysinfo"
)

func main() {
	// The Cocoa event loop has to run on the main thread, so the main
	// goroutine is pinned to it.
	runtime.LockOSThread()

	var (
		noBrowser = flag.Bool("no-browser", false, "do not open the browser on start")
		noMenuBar = flag.Bool("no-menubar", false, "do not show the menu bar item")
		port      = flag.Int("port", config.DefaultPort, "TCP port on 127.0.0.1")
		version   = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *version {
		fmt.Println("mymac", config.Version)
		return
	}

	if err := config.EnsureDirs(); err != nil {
		fmt.Fprintln(os.Stderr, "mymac:", err)
		os.Exit(1)
	}

	// One instance per user. A second one would fight over the same port
	// and the same config files.
	lock, err := instance.Acquire(config.LockFile())
	if err != nil {
		if errors.Is(err, instance.ErrRunning) {
			reportRunning()
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, "mymac:", err)
		os.Exit(1)
	}
	defer lock.Release()

	logger := server.NewLogger()
	al := audit.New(config.AuditLog())

	collector := procs.NewCollector(2 * time.Second)
	collector.SetPortsEnabled(true)
	collector.SetNetEnabled(true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	collector.StartArchWarmer(ctx)
	collector.StartAppWarmer(ctx)
	collector.StartBackground(ctx)

	envManager := envs.NewManager(envs.Options{Audit: al})
	envManager.SetProcessLister(collector)

	appScanner := apps.NewScanner()
	appScanner.StartSizeWarmer(ctx)

	alerter := procs.NewAlerter(procs.OSNotifier{}, procs.DefaultRules())
	alerter.Start(ctx, collector, 5*time.Second)

	srv, err := server.New(server.Options{
		Audit:   al,
		Logger:  logger,
		Env:     envManager,
		Procs:   collector,
		Sysinfo: sysinfo.NewCollector(),
		Apps:    appScanner,
		Alerter: alerter,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "mymac:", err)
		os.Exit(1)
	}
	if err := srv.ListenOn(*port); err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			fmt.Fprintf(os.Stderr, "mymac: 端口 %d 已被占用\n", *port)
		} else {
			fmt.Fprintf(os.Stderr, "mymac: 无法监听端口 %d：%v\n", *port, err)
		}
		os.Exit(1)
	}

	addr := srv.Addr()
	if err := lock.WriteInfo(instance.Info{PID: os.Getpid(), Port: srv.Port(), Addr: addr}); err != nil {
		logger.Printf("could not record the instance info: %v", err)
	}
	logger.Printf("listening on %s", addr)

	// The server runs on its own goroutine; the main thread belongs to the
	// menu bar.
	serveErr := make(chan error, 1)
	serveDone := make(chan struct{})
	go func() {
		err := srv.Serve(ctx, srv.Listener())
		serveErr <- err
		close(serveDone)
	}()

	quit := make(chan struct{})
	var quitOnce sync.Once
	askQuit := func() {
		quitOnce.Do(func() {
			logger.Printf("shutting down")
			cancel()
			close(quit)
		})
	}

	// Signals arrive on a goroutine: the main thread is busy in the Cocoa
	// loop and cannot wait for them.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		logger.Printf("received %v, shutting down", sig)
		askQuit()
		<-serveDone
		os.Exit(0)
	}()

	var menu *menubar.Menu
	if !*noMenuBar {
		menu, err = menubar.New(menubar.Options{
			Title:   "打开 mymac 界面",
			Address: addr,
			OnOpen:  func() { go func() { _ = server.OpenBrowser(addr) }() },
			OnQuit:  askQuit,
		})
		if err != nil {
			logger.Printf("menu bar unavailable: %v", err)
			menu = nil
		}
	}

	if !*noBrowser {
		if err := server.OpenBrowser(addr); err != nil {
			logger.Printf("could not open browser: %v", err)
		}
	}
	fmt.Printf("mymac %s running at %s\n", config.Version, addr)

	if menu != nil {
		// The Cocoa loop owns the main thread until the user quits.
		menu.Run()
	}
	<-quit

	select {
	case err := <-serveErr:
		if err != nil {
			fmt.Fprintln(os.Stderr, "mymac:", err)
			os.Exit(1)
		}
	case <-time.After(6 * time.Second):
		logger.Printf("shutdown timed out")
	}
}

// reportRunning tells the user where the running instance lives.
func reportRunning() {
	info, err := instance.ReadInfo(config.LockFile())
	if err != nil || info.Addr == "" {
		fmt.Fprintln(os.Stderr, "mymac 已在运行")
		return
	}
	fmt.Fprintf(os.Stderr, "mymac 已在运行（PID %d）：%s\n", info.PID, info.Addr)
}
