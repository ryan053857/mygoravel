package split_test

import (
	"fmt"

	"github.com/go-tenon/tenon/document/split"
)

func Example() {
	splitter, err := split.New(split.Options{Size: 40, Overlap: 10})
	if err != nil {
		panic(err)
	}
	text := "# Guide\n\n## Install\n\nRun the installer. It takes a minute. Then restart your shell.\n"
	for _, chunk := range splitter.Split(text) {
		fmt.Printf("[%d:%d] %q\n", chunk.Start, chunk.End, chunk.TextWithContext())
	}
	// Output:
	// [0:40] "# Guide\n\n## Install\n\nRun the installer. "
	// [40:84] "Guide > Install\n\nIt takes a minute. Then restart your shell.\n"
}
