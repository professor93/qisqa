// qisqa is a URL shortener: one binary that keeps its links in its own data
// folder.
//
// This file only starts the program, which is the package internal/app.
package main

import (
	"os"

	"github.com/professor93/qisqa/internal/app"
)

// version is set when a release is built:
//
//	go build -ldflags "-X main.version=1.2.3"
var version = "dev"

func main() {
	os.Exit(app.Main(version, os.Args[1:], os.Stdout, os.Stderr))
}
