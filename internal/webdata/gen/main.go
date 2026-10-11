// Command gen downloads the HTML and CSS data of microsoft/vscode-custom-data (MIT) and stores it
// gzipped next to the webdata package. Run with: scripts/go.sh generate ./internal/webdata/
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
)

const base = "https://raw.githubusercontent.com/microsoft/vscode-custom-data/main/web-data/data/"

func main() {
	for _, f := range []struct{ url, out string }{
		{base + "browsers.html-data.json", "html-data.json.gz"},
		{base + "browsers.css-data.json", "css-data.json.gz"},
		{"https://raw.githubusercontent.com/microsoft/vscode-custom-data/main/LICENSE", "LICENSE.vscode-custom-data"},
	} {
		if err := fetch(f.url, f.out); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func fetch(url, out string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if len(out) < 3 || out[len(out)-3:] != ".gz" {
		return os.WriteFile(out, data, 0o644)
	}
	// Re-encode compactly before compressing.
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	compact, _ := json.Marshal(v)
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	zw.Write(compact)
	zw.Close()
	fmt.Printf("%s: %d bytes (%d raw)\n", out, buf.Len(), len(data))
	return os.WriteFile(out, buf.Bytes(), 0o644)
}
