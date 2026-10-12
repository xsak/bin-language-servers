// Command mkzip packs a directory tree into a zip archive with a stable entry order.
// All entries are placed under a top-level directory named after <dir>, the same way
// `tar -czf out.tar.gz <dir>` packs it. It exists so release builds (scripts/dist.sh)
// do not need host-side zip tools.
//
//	mkzip <dir> <out.zip>
package main

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

type entry struct {
	archive string // name inside the zip, under the top-level directory
	rel     string // path relative to the walked root
	perm    os.FileMode
}

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: mkzip <dir> <out.zip>")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2]); err != nil {
		fmt.Fprintln(os.Stderr, "mkzip:", err)
		os.Exit(1)
	}
}

func run(root, out string) error {
	top := filepath.ToSlash(filepath.Base(filepath.Clean(root)))
	var entries []entry
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() {
			rel, err := filepath.Rel(root, p)
			if err != nil {
				return err
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			entries = append(entries, entry{
				archive: top + "/" + filepath.ToSlash(rel),
				rel:     rel,
				perm:    fi.Mode().Perm(),
			})
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].archive < entries[j].archive })

	f, err := os.Create(out)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	for _, e := range entries {
		src, err := os.Open(filepath.Join(root, e.rel))
		if err != nil {
			src.Close()
			return err
		}
		hdr := &zip.FileHeader{Name: e.archive, Method: zip.Deflate}
		hdr.SetMode(0o600 | e.perm)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			src.Close()
			return err
		}
		_, err = io.Copy(w, src)
		src.Close()
		if err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
