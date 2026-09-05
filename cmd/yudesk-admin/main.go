package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/yudesk/yudesk/internal/account"
)

func main() {
	action := "generate"
	args := os.Args[1:]
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		action = args[0]
		args = args[1:]
	}
	switch action {
	case "generate":
		generate(args)
	case "list":
		list(args)
	case "revoke":
		revoke(args)
	default:
		usage()
		os.Exit(2)
	}
}

func generate(args []string) {
	flags := flag.NewFlagSet("generate", flag.ExitOnError)
	database := flags.String("database", "yudesk.db", "relay SQLite database")
	days := flags.Int("days", 30, "licensed usage duration in days")
	hours := flags.Int("hours", 0, "additional licensed usage duration in hours")
	count := flags.Int("count", 1, "number of activation keys to generate")
	redeemDays := flags.Int("redeem-within", 90, "key must be redeemed within this many days")
	_ = flags.Parse(args)
	duration := time.Duration(*days*24+*hours) * time.Hour
	if duration < time.Hour || *count <= 0 || *count > 1000 || *redeemDays <= 0 {
		log.Fatal("duration and redeem window must be positive; count must be 1-1000")
	}
	store := open(*database)
	defer store.Close()
	for i := 0; i < *count; i++ {
		key, err := store.GenerateActivationKey(duration, time.Duration(*redeemDays)*24*time.Hour)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println(key)
	}
}

func list(args []string) {
	flags := flag.NewFlagSet("list", flag.ExitOnError)
	database := flags.String("database", "yudesk.db", "relay SQLite database")
	limit := flags.Int("limit", 100, "maximum activation keys to list")
	_ = flags.Parse(args)
	store := open(*database)
	defer store.Close()
	keys, err := store.ListActivationKeys(*limit)
	if err != nil {
		log.Fatal(err)
	}
	for _, key := range keys {
		state := "unused"
		when := "-"
		if !key.RevokedAt.IsZero() {
			state, when = "revoked", key.RevokedAt.Format(time.RFC3339)
		} else if key.RedeemedBy != "" {
			state, when = "used by "+key.RedeemedBy, key.RedeemedAt.Format(time.RFC3339)
		} else if time.Now().After(key.RedeemBy) {
			state = "expired"
		}
		fmt.Printf("%s\t%s\t%s\tredeem-by=%s\t%s\n", key.Fingerprint, key.Duration, state, key.RedeemBy.Format(time.RFC3339), when)
	}
}

func revoke(args []string) {
	flags := flag.NewFlagSet("revoke", flag.ExitOnError)
	database := flags.String("database", "yudesk.db", "relay SQLite database")
	key := flags.String("key", "", "unused activation key to revoke")
	_ = flags.Parse(args)
	if *key == "" {
		log.Fatal("-key is required")
	}
	store := open(*database)
	defer store.Close()
	if err := store.RevokeActivationKey(*key); err != nil {
		log.Fatal(err)
	}
	fmt.Println("activation key revoked")
}

func open(database string) *account.Store {
	store, err := account.Open(database)
	if err != nil {
		log.Fatal(err)
	}
	return store
}

func usage() {
	fmt.Println("Usage: yudesk-admin generate|list|revoke [options]")
}
