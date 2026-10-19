// The helper signs only this runner's disposable cache configuration.
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
	path := flag.String("config", "", "disposable configuration")
	capability := flag.String("capability", "read", "read or write")
	project := flag.String("project", "", "optional foreign project control")
	profile := flag.String("profile", "", "optional changed profile control")
	integration := flag.String("integration", "bazel", "native integration")
	flag.Parse()
	cfg, err := config.Load(*path)
	if err != nil {
		fail(err)
	}
	if *capability != "read" && *capability != "write" {
		fail(fmt.Errorf("read or write capability required"))
	}
	if *project == "" {
		*project = cfg.ProjectID
	}
	if *profile == "" {
		*profile = cfg.CompatibilityID
	}
	now := time.Now().UTC()
	token, err := access.MintCapabilityToken(cfg.LocalToken, access.Claims{
		Subject: "github:qa", Project: *project, Integration: *integration, Compatibility: *profile,
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
