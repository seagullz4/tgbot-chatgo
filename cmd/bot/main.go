package main

import (
	"fmt"
	"os"

	"telegram-interactive-bot/go-bot/internal/app"
)

func main() {
	if err := app.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
