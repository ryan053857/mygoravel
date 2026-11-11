package convert_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/ryan053857/mygoravel/document/convert"
	"github.com/ryan053857/mygoravel/document/convert/text"
)

func ExampleRegistry() {
	var registry convert.Registry
	registry.Register(text.New(text.Options{}), convert.Text, convert.Markdown)
	registry.Register(text.JSON(text.Options{}), convert.JSON)

	format, _ := convert.FormatOf("settings.json")
	doc, err := registry.Convert(context.Background(), format, convert.Input{Reader: strings.NewReader(`{"debug": true}`)})
	if err != nil {
		panic(err)
	}
	fmt.Println(doc.Markdown)
	// Output:
	// ```json
	// {"debug": true}
	// ```
}
