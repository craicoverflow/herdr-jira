package main

import (
	"context"
	"os/exec"
	"time"
)

const browserWait = 5 * time.Second

func openBrowser(u string) error {
	ctx, cancel := context.WithTimeout(context.Background(), browserWait)
	defer cancel()
	return exec.CommandContext(ctx, "open", u).Run()
}
