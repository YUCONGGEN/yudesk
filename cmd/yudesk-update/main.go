package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yudesk/yudesk/internal/updater"
)

func main() {
	manifestURL := flag.String("manifest", "", "HTTPS URL of the signed update manifest")
	publicKeyText := flag.String("public-key", "", "base64 Ed25519 release public key")
	component := flag.String("component", "yudesk-agent", "component to update")
	destination := flag.String("destination", "", "binary destination path")
	allowHTTP := flag.Bool("allow-http", false, "allow HTTP for local development")
	checkOnly := flag.Bool("check", false, "verify manifest and report the matching release without installing")
	flag.Parse()
	if *manifestURL == "" || *publicKeyText == "" {
		log.Fatal("-manifest and -public-key are required")
	}
	publicKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(*publicKeyText))
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		log.Fatal("invalid Ed25519 public key")
	}
	if *destination == "" {
		name := *component
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		*destination = filepath.Join(filepath.Dir(os.Args[0]), name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	data, err := updater.Fetch(ctx, *manifestURL, *allowHTTP)
	if err != nil {
		log.Fatal(err)
	}
	manifest, err := updater.Verify(data, ed25519.PublicKey(publicKey))
	if err != nil {
		log.Fatal(err)
	}
	file, err := updater.Select(manifest, *component, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		log.Fatal(err)
	}
	if *checkOnly {
		fmt.Printf("verified %s %s for %s/%s\n", *component, manifest.Version, runtime.GOOS, runtime.GOARCH)
		return
	}
	versionFile := *destination + ".version"
	if current, readErr := os.ReadFile(versionFile); readErr == nil && strings.TrimSpace(string(current)) == manifest.Version {
		fmt.Printf("%s is already at %s\n", *component, manifest.Version)
		return
	}
	backup, err := updater.Install(ctx, file, *destination, *allowHTTP)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(versionFile, []byte(manifest.Version+"\n"), 0644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("installed %s %s", *component, manifest.Version)
	if backup != "" {
		fmt.Printf("; previous binary: %s", backup)
	}
	fmt.Println()
}
