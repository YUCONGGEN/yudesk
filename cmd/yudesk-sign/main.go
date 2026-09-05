package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yudesk/yudesk/internal/updater"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "keygen":
		keygen(os.Args[2:])
	case "manifest":
		manifest(os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
}

func keygen(args []string) {
	flags := flag.NewFlagSet("keygen", flag.ExitOnError)
	privatePath := flags.String("private", "release-private.key", "private signing key output")
	publicPath := flags.String("public", "release-public.key", "public verification key output")
	force := flags.Bool("force", false, "overwrite existing key files")
	_ = flags.Parse(args)
	if !*force && (exists(*privatePath) || exists(*publicPath)) {
		log.Fatal("key file already exists; refusing to overwrite it")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*privatePath, []byte(base64.StdEncoding.EncodeToString(privateKey)+"\n"), 0600); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(*publicPath, []byte(base64.StdEncoding.EncodeToString(publicKey)+"\n"), 0644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("created %s and %s\n", *privatePath, *publicPath)
}

func manifest(args []string) {
	flags := flag.NewFlagSet("manifest", flag.ExitOnError)
	dist := flags.String("dist", "dist", "release artifact directory")
	baseURL := flags.String("base-url", "", "HTTPS base URL containing release artifacts")
	version := flags.String("version", "", "release version")
	privatePath := flags.String("private", "release-private.key", "private signing key")
	output := flags.String("output", "update-manifest.json", "signed manifest output")
	_ = flags.Parse(args)
	parsedBase, err := url.Parse(*baseURL)
	if err != nil || parsedBase.Scheme != "https" || parsedBase.Host == "" {
		log.Fatal("-base-url must be an HTTPS URL")
	}
	if strings.TrimSpace(*version) == "" {
		log.Fatal("-version is required")
	}
	privateText, err := os.ReadFile(*privatePath)
	if err != nil {
		log.Fatal(err)
	}
	privateKey, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(privateText)))
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		log.Fatal("invalid Ed25519 private key")
	}
	root, err := filepath.Abs(*dist)
	if err != nil {
		log.Fatal(err)
	}
	var files []updater.File
	err = filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() == "SHA256SUMS.txt" {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		if len(parts) != 2 {
			return nil
		}
		platform := strings.SplitN(parts[0], "-", 2)
		if len(platform) != 2 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(data)
		component := strings.TrimSuffix(parts[1], ".exe")
		artifactURL, err := url.JoinPath(strings.TrimRight(*baseURL, "/"), parts[0], parts[1])
		if err != nil {
			return err
		}
		files = append(files, updater.File{Component: component, OS: platform[0], Arch: platform[1], URL: artifactURL, SHA256: hex.EncodeToString(hash[:])})
		return nil
	})
	if err != nil {
		log.Fatal(err)
	}
	if len(files) == 0 {
		log.Fatal("no release artifacts found")
	}
	sort.Slice(files, func(i, j int) bool {
		left := files[i].Component + files[i].OS + files[i].Arch
		right := files[j].Component + files[j].OS + files[j].Arch
		return left < right
	})
	document := updater.Manifest{Version: *version, PublishedAt: time.Now().UTC(), Files: files}
	payload, err := updater.SigningPayload(document)
	if err != nil {
		log.Fatal(err)
	}
	document.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(privateKey), payload))
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(*output, encoded, 0644); err != nil {
		log.Fatal(err)
	}
	fmt.Printf("signed %d artifacts into %s\n", len(files), *output)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func usage() {
	fmt.Println("Usage: yudesk-sign keygen|manifest [options]")
}
