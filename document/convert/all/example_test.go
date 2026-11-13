package all_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-tenon/tenon/document/convert"
	"github.com/go-tenon/tenon/document/convert/all"
	"github.com/go-tenon/tenon/document/split"
)

func Example() {
	converters := all.New(all.Options{})
	defer func() { _ = converters.Close() }()

	format, _ := convert.FormatOf("guide.html")
	doc, err := converters.Convert(context.Background(), format, convert.Input{
		Reader: strings.NewReader("<h1>Guide</h1><h2>Install</h2><p>Run the installer. Then restart your shell.</p>"),
	})
	if err != nil {
		panic(err)
	}
	splitter, err := split.New(split.Options{Size: 500})
	if err != nil {
		panic(err)
	}
	for _, chunk := range splitter.Split(doc.Markdown) {
		fmt.Printf("%q\n", chunk.TextWithContext())
	}
	// Output:
	// "# Guide\n\n## Install\n\nRun the installer. Then restart your shell."
}
