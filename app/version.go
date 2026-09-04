package main

import (
	"os"
	"strings"
)

// Version can be overridden at build time: -ldflags "-X main.Version=..."
var Version = "dev"

func VersionString() string {
	if v := strings.TrimSpace(os.Getenv("DISTORTIONER_VERSION")); v != "" {
		return v
	}
	return Version
}
