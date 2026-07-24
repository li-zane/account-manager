package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	postgresrepo "github.com/li-zane/account-manager/backend/internal/repository/postgres"
)

func main() {
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		fatalf("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	store, err := postgresrepo.Open(ctx, databaseURL)
	if err != nil {
		fatalf("open database: %v", err)
	}
	defer store.Close()
	if err := store.Migrate(ctx); err != nil {
		fatalf("apply migration: %v", err)
	}
	fmt.Println("database migrations are current")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
