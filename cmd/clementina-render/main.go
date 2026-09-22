// Command clementina-render converts a JSON array of video bytes on stdin to PNG on stdout.
package main

import (
	"encoding/json"
	"fmt"
	"github.com/fran150/clementina-video-client/pkg/render"
	"image/png"
	"io"
	"os"
)

func run() error {
	var values []int
	d := json.NewDecoder(io.LimitReader(os.Stdin, 1048576))
	if err := d.Decode(&values); err != nil {
		return err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return fmt.Errorf("expected one video snapshot")
	}
	data := make([]byte, len(values))
	for i, v := range values {
		if v < 0 || v > 255 {
			return fmt.Errorf("invalid video byte")
		}
		data[i] = byte(v)
	}
	img, err := render.Snapshot(data)
	if err != nil {
		return err
	}
	return png.Encode(os.Stdout, img)
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
