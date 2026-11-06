// Command dzcollab is the reference server for collaborative editing in dzStudio (protocol dzcollab 1).
//
//	dzcollab [config.json]          (or DZCOLLAB_CONFIG=config.json)
//	dzcollab health [config.json]   exits 0 if the server answers info (for container health checks)
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/siemendev/dzstudio-collab-server/config"
	"github.com/siemendev/dzstudio-collab-server/httpapi"
	"github.com/siemendev/dzstudio-collab-server/reference"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Println("usage: dzcollab [config.json]   (or DZCOLLAB_CONFIG; DZCOLLAB_PORT, DZCOLLAB_DATA_DIR, DZCOLLAB_PUBLIC_URL override it)")
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "health" {
		os.Exit(health())
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func health() int {
	path := os.Getenv("DZCOLLAB_CONFIG")
	if len(os.Args) > 2 {
		path = os.Args[2]
	}
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	host := cfg.Listen.Host
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 4 * time.Second}
	res, err := client.Get("http://" + net.JoinHostPort(host, strconv.Itoa(cfg.Listen.Port)) + httpapi.NormalizeBasePath(cfg.Listen.BasePath) + "info")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer res.Body.Close()
	buf := make([]byte, 64)
	n, _ := io.ReadFull(res.Body, buf)
	if !strings.HasPrefix(string(buf[:n]), "dzcollab 1\n{\"ok\":true") {
		fmt.Fprintln(os.Stderr, "unexpected answer")
		return 1
	}
	return 0
}

func logLine(line string) {
	fmt.Fprintf(os.Stdout, "%s %s\n", time.Now().UTC().Format(time.RFC3339), line)
}

func run() error {
	path := os.Getenv("DZCOLLAB_CONFIG")
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	cfg, err := config.Load(path)
	if err != nil {
		return err
	}
	if cfg.Testing.Enabled {
		logLine("WARNING: testing.enabled is on — test controls are reachable; never use this for real sessions")
	}
	srv, err := reference.New(cfg, logLine)
	if err != nil {
		return err
	}
	if err := srv.Service.Start(); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(cfg.Listen.Host, strconv.Itoa(cfg.Listen.Port)))
	if err != nil {
		return err
	}
	hs := &http.Server{Handler: srv.Handler, ReadHeaderTimeout: 20 * time.Second, IdleTimeout: 15 * time.Second}
	host := cfg.Listen.Host
	if host == "0.0.0.0" || host == "::" || host == "" {
		host = "127.0.0.1"
	}
	port := ln.Addr().(*net.TCPAddr).Port
	label := path
	if label == "" {
		label = "defaults"
	}
	logLine(fmt.Sprintf("dzcollab server %q listening on http://%s%s (config: %s)", cfg.Server.Name,
		net.JoinHostPort(host, strconv.Itoa(port)), httpapi.NormalizeBasePath(cfg.Listen.BasePath), label))

	errc := make(chan error, 1)
	go func() { errc <- hs.Serve(ln) }()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case s := <-sig:
		logLine(s.String() + ": stopping")
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	srv.Service.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = hs.Shutdown(ctx)
	return nil
}
