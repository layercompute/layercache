// The helper signs short-lived credentials only for this runner's disposable
// configurations. It never reads or changes operator configuration.
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/layercache/layercache/internal/access"
	"github.com/layercache/layercache/internal/config"
	"github.com/layercache/layercache/internal/execution"
)

func main() {
	path := flag.String("config", "", "disposable cache configuration")
	capability := flag.String("capability", "read", "read or write")
	compatibility := flag.String("compatibility", "", "isolated profile")
	runID := flag.String("run", "", "optional isolated member-local run nonce")
	workspace := flag.String("workspace", "", "optional opaque isolated workspace identity")
	flag.Parse()
	if *path == "" || (*capability != "read" && *capability != "write") {
		fail(fmt.Errorf("config and read/write capability are required"))
	}
	cfg, err := config.Load(*path)
	if err != nil {
		fail(err)
	}
	profile := cfg.CompatibilityID
	if *compatibility != "" {
		profile = *compatibility
	}
	now := time.Now().UTC()
	claims := access.Claims{
		Subject: "github:qa", Project: cfg.ProjectID, Integration: "gradle", Compatibility: profile,
		Capabilities: []access.Capability{access.Capability(*capability)}, ExpiresAt: now.Add(time.Hour),
	}
	if *runID != "" || *workspace != "" {
		if *runID == "" || *workspace == "" {
			fail(fmt.Errorf("run and workspace must be supplied together"))
		}
		claims.RunID = "github-local:github:qa:" + *runID
		claims.WorkspaceID = "github-local:github:qa:" + *workspace
		claims.Repository = "layercache/gradle-native-qa"
		claims.SourceCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		claims.Execution = execution.ClientContext("github-id:42", *runID)
	}
	token, err := access.MintCapabilityToken(cfg.LocalToken, claims, now)
	if err != nil {
		fail(err)
	}
	fmt.Println(token)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
