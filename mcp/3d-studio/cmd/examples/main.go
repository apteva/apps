// Generate gallery thumbnails from the editable examples, never from mockups.
package main

import (
	"github.com/apteva/apps/mcp/3d-studio/engine"
	"log"
	"os"
	"path/filepath"
)

func main() {
	output := "ui/examples"
	if len(os.Args) > 1 {
		output = os.Args[1]
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		log.Fatal(err)
	}
	for _, example := range []struct {
		name  string
		build func() (engine.Document, error)
	}{{"car", engine.ExampleCar}, {"warrior", engine.ExampleWarrior}, {"landscape", engine.ExampleLandscape}, {"sword", engine.ExampleSword}} {
		doc, err := example.build()
		if err != nil {
			log.Fatal(example.name, ": ", err)
		}
		data, err := engine.RenderPNG(doc, "perspective", nil)
		if err != nil {
			log.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(output, example.name+".png"), data, 0644); err != nil {
			log.Fatal(err)
		}
	}
}
