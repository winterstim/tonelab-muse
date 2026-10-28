// Command riff writes the built-in guitar riff as a clean DI WAV.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/winterstim/tonelab-muse/pkg/riff"
)

func main() {
	secs := flag.Float64("seconds", 16, "length")
	rate := flag.Int("rate", 48000, "sample rate")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: riff [-seconds 16] out.wav")
		os.Exit(2)
	}
	if err := riff.WriteWAV(flag.Arg(0), riff.Generate(*rate, *secs), *rate); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
