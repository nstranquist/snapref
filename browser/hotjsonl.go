package browser

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const hotJSONLDefaultMax int64 = 64 << 10

func hotJSONLMaxBytes() int64 {
	raw := strings.TrimSpace(os.Getenv("NDEV_JSONL_MAX_BYTES"))
	if raw == "" {
		return hotJSONLDefaultMax
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return hotJSONLDefaultMax
	}
	return n
}

func appendAndRotateJSONL(path string, body []byte, perm os.FileMode) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("hotjsonl: empty path")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	line := append(append([]byte(nil), body...), '\n')
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		_ = f.Close()
		return err
	}
	_, werr := f.Write(line)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}
	return rotateJSONLIfNeeded(path)
}

func rotateJSONLIfNeeded(path string) error {
	max := hotJSONLMaxBytes()
	if max == 0 {
		return nil
	}
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if fi.Size() <= max {
		return nil
	}
	dest, err := uniqueJSONLRotationDest(path)
	if err != nil {
		return err
	}
	if err := os.Rename(path, dest); err != nil {
		return err
	}
	if err := gzipJSONLFile(dest); err != nil {
		return err
	}
	return os.WriteFile(path, nil, fi.Mode().Perm())
}

func uniqueJSONLRotationDest(path string) (string, error) {
	stamp := time.Now().UTC().Format("20060102T150405Z")
	base := path + "." + stamp
	for i := 0; i < 1000; i++ {
		dest := base
		if i > 0 {
			dest = base + "-" + strconv.Itoa(i)
		}
		if _, err := os.Stat(dest); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		if _, err := os.Stat(dest + ".gz"); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return "", err
		}
		return dest, nil
	}
	return "", fmt.Errorf("hotjsonl: no free rotation name for %s", path)
}

func gzipJSONLFile(src string) error {
	if strings.HasSuffix(src, ".gz") {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(src + ".gz")
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(out)
	_, copyErr := io.Copy(zw, in)
	closeErr := zw.Close()
	outErr := out.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if outErr != nil {
		return outErr
	}
	return os.Remove(src)
}

func jsonlFamily(live string) []string {
	if strings.TrimSpace(live) == "" {
		return nil
	}
	matches, _ := filepath.Glob(live + ".*")
	out := make([]string, 0, len(matches)+1)
	for _, p := range matches {
		if !isJSONLRotationSibling(live, p) {
			continue
		}
		out = append(out, p)
	}
	if _, err := os.Stat(live); err == nil {
		out = append(out, live)
	}
	return out
}

func isJSONLRotationSibling(live, path string) bool {
	if path == live {
		return false
	}
	suffix := strings.TrimPrefix(path, live+".")
	if suffix == path {
		return false
	}
	suffix = strings.TrimSuffix(suffix, ".gz")
	if suffix == "lock" {
		return false
	}
	if _, err := time.Parse("20060102T150405Z", suffix); err == nil {
		return true
	}
	dash := strings.LastIndex(suffix, "-")
	if dash <= 0 {
		return false
	}
	if _, err := time.Parse("20060102T150405Z", suffix[:dash]); err != nil {
		return false
	}
	n, err := strconv.Atoi(suffix[dash+1:])
	return err == nil && n > 0
}

func forEachJSONLFamilyLine(live string, fn func(line []byte) error) error {
	for _, p := range jsonlFamily(live) {
		if err := forEachJSONLLine(p, fn); err != nil {
			return err
		}
	}
	return nil
}

func forEachJSONLLine(path string, fn func(line []byte) error) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return err
		}
		defer gz.Close()
		r = gz
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if err := fn([]byte(line)); err != nil {
			return err
		}
	}
	return sc.Err()
}
