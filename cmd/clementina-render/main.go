// Command clementina-render turns a snapshot of MIA's video state into a PNG
// on stdout. With a FILE argument it reads the snapshot's raw bytes from that
// file, such as the .video.bin files the Clementina SDK saves; without one it
// reads a JSON array of the bytes from stdin.
package main

import (
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"os"

	"github.com/fran150/clementina-video-client/internal/buildinfo"
	"github.com/fran150/clementina-video-client/pkg/render"
)

const usage = `usage: clementina-render [FILE] > frame.png
  FILE         raw video snapshot bytes (for example a .video.bin file)
  no FILE      a JSON array of the snapshot bytes on stdin
  --version    print the version and exit`

// readJSON decodes one JSON array of video bytes.
func readJSON(r io.Reader) ([]byte, error) {
	var values []int
	d := json.NewDecoder(io.LimitReader(r, 1048576))
	if err := d.Decode(&values); err != nil {
		return nil, err
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, fmt.Errorf("expected one video snapshot")
	}
	data := make([]byte, len(values))
	for i, v := range values {
		if v < 0 || v > 255 {
			return nil, fmt.Errorf("invalid video byte")
		}
		data[i] = byte(v)
	}
	return data, nil
}

func run(args []string) error {
	var data []byte
	var err error
	switch len(args) {
	case 0:
		data, err = readJSON(os.Stdin)
	case 1:
		data, err = os.ReadFile(args[0])
	default:
		return fmt.Errorf("%s", usage)
	}
	if err != nil {
		return err
	}
	img, err := render.Snapshot(data)
	if err != nil {
		return err
	}
	return png.Encode(os.Stdout, img)
}

func main() {
	args := os.Args[1:]
	if len(args) == 1 {
		switch args[0] {
		case "--version", "-version":
			fmt.Println(buildinfo.String("clementina-render"))
			return
		case "--help", "-help", "-h":
			fmt.Println(usage)
			return
		}
	}
	if err := run(args); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
