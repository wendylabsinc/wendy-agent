package main

import (
	"fmt"
	"github.com/wendylabsinc/wendy/internal/cli/tegraflash/bundle"
	"os"
	"strings"
)

func main() {
	b, err := bundle.Open(os.Args[1])
	if err != nil {
		panic(err)
	}
	if len(os.Args) > 2 && !strings.Contains(os.Args[2], "=") {
		data, err := b.ExtractFile(os.Args[2])
		if err != nil {
			panic(err)
		}
		os.Stdout.Write(data)
		return
	}
	files, _ := b.ListFiles()
	for _, f := range files {
		if len(os.Args) > 2 {
			filter := os.Args[2]
			if !strings.Contains(strings.ToLower(f), strings.ToLower(filter)) {
				continue
			}
		}
		data, _ := b.ExtractFile(f)
		fmt.Printf("%8d  %s\n", len(data), f)
	}
}
