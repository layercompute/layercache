// This QA helper mints short-lived native Nx credentials from a disposable
// server's protected configuration. It is never an application authentication
// endpoint and does not print the server secret.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
)

func main() {
	configPath := flag.String("config", "", "disposable Team Cache configuration")
	capability := flag.String("capability", "read", "read or write")
	compatibility := flag.String("compatibility", "", "optional isolated compatibility profile")
	flag.Parse()
	if *configPath == "" || (*capability != "read" && *capability != "write") {
		fail(fmt.Errorf("config and read/write capability are required"))
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fail(err)
	}
	profile := cfg.CompatibilityID
	if *compatibility != "" {
		profile = *compatibility
	}
	now := time.Now().UTC()
	token, err := access.MintCapabilityToken(cfg.LocalToken, access.Claims{
		Subject: "qa:nx", Project: cfg.ProjectID, Integration: "nx", Compatibility: profile,
		Capabilities: []access.Capability{access.Capability(*capability)}, ExpiresAt: now.Add(time.Hour),
	}, now)
	if err != nil {
		fail(err)
	}
	fmt.Println(token)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
