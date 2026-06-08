// perfmerge: deterministic JSON-aware merge of two performance.json
// files. Used as a git custom merge driver or invoked manually via
// `make merge-performance` (§5.4).
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/hmchangw/chat/tools/archived/integration-suite/internal/runtime"
)

func main() {
	left := flag.String("left", "", "left side path")
	right := flag.String("right", "", "right side path")
	out := flag.String("out", "", "output path (defaults to left)")
	flag.Parse()
	if *left == "" || *right == "" {
		fmt.Fprintln(os.Stderr, "usage: perfmerge -left <path> -right <path> [-out <path>]")
		os.Exit(2)
	}
	if *out == "" {
		*out = *left
	}

	a, err := runtime.LoadPerformance(*left)
	exitOn(err)
	b, err := runtime.LoadPerformance(*right)
	exitOn(err)
	merged := runtime.Merge(a, b)
	exitOn(runtime.SavePerformance(*out, merged))
}

func exitOn(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
