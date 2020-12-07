package main

import (
	"fmt"
	"testing"

	"uk.ac.bris.cs/gameoflife/gol"
)

func BenchmarkParallel(b *testing.B) {
	//os.Stdout = nil
	testParams := gol.Params{ImageWidth: 512, ImageHeight: 512, Turns: 10000}
	for i := 0; i < b.N; i++ {

		for threads := 1; threads <= 16; threads++ {

			testArgument := fmt.Sprint(threads)
			b.Run(testArgument, func(b *testing.B) {
				testParams.Threads = threads
				events := make(chan gol.Event)
				gol.Run(testParams, events, nil)
				for range events {
				}

			})
		}
	}

}
