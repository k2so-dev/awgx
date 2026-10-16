package backup

import (
	"archive/tar"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
)

func Create(dir string, paths []string, snapshots map[string]string) (string, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	base := filepath.Join(dir, "awgx-backup-"+time.Now().UTC().Format("20060102-150405"))
	file := base + ".tar.zst"
	f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	for n := 1; os.IsExist(err) && n < 100; n++ {
		file = fmt.Sprintf("%s-%d.tar.zst", base, n)
		f, err = os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
	}
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(file)
		}
	}()
	zw, err := zstd.NewWriter(f)
	if err != nil {
		return "", err
	}
	tw := tar.NewWriter(zw)
	for _, p := range paths {
		if err := addPath(tw, p, snapshots); err != nil {
			return "", fmt.Errorf("backup %s: %w", p, err)
		}
	}
	if err := tw.Close(); err != nil {
		return "", err
	}
	if err := zw.Close(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	ok = true
	return file, nil
}

func addPath(tw *tar.Writer, root string, snapshots map[string]string) error {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return nil
	}
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink != 0 || strings.HasSuffix(path, ".lock") {
			return nil
		}
		if db := strings.TrimSuffix(strings.TrimSuffix(path, "-wal"), "-shm"); db != path && snapshots[db] != "" {
			return nil
		}
		src := path
		if s := snapshots[path]; s != "" {
			src = s
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		if src != path {
			snap, err := os.Stat(src)
			if err != nil {
				return err
			}
			hdr.Size = snap.Size()
		}
		hdr.Name = strings.TrimPrefix(path, "/")
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		in, err := os.Open(src)
		if err != nil {
			return err
		}
		defer in.Close()
		_, err = io.Copy(tw, in)
		return err
	})
}

func Verify(archive string, allow []string) error {
	return walk(archive, allow, func(name string, hdr *tar.Header, r io.Reader) error {
		_, err := io.Copy(io.Discard, r)
		return err
	})
}

func Restore(archive, root string, allow []string) ([]string, error) {
	if err := Verify(archive, allow); err != nil {
		return nil, err
	}
	var restored []string
	err := walk(archive, allow, func(name string, hdr *tar.Header, r io.Reader) error {
		target := filepath.Join(root, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			return os.MkdirAll(target, os.FileMode(hdr.Mode)&0o777)
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, r); err != nil {
				out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
			restored = append(restored, target)
		}
		return nil
	})
	return restored, err
}

func walk(archive string, allow []string, fn func(name string, hdr *tar.Header, r io.Reader) error) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	zr, err := zstd.NewReader(f)
	if err != nil {
		return err
	}
	defer zr.Close()
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := filepath.Clean("/" + hdr.Name)
		if !allowed(name, allow) {
			return fmt.Errorf("backup entry %s is outside the awgx directories", hdr.Name)
		}
		if err := fn(name, hdr, tr); err != nil {
			return err
		}
	}
}

func Prune(dir string, keep int) error {
	files, err := filepath.Glob(filepath.Join(dir, "awgx-backup-*.tar.zst"))
	if err != nil || len(files) <= keep {
		return err
	}
	mod := map[string]time.Time{}
	for _, f := range files {
		if st, err := os.Stat(f); err == nil {
			mod[f] = st.ModTime()
		}
	}
	sort.Slice(files, func(i, j int) bool { return mod[files[i]].After(mod[files[j]]) })
	var errs []error
	for _, f := range files[keep:] {
		errs = append(errs, os.Remove(f))
	}
	return errors.Join(errs...)
}

func allowed(name string, allow []string) bool {
	for _, a := range allow {
		if name == a || strings.HasPrefix(name, strings.TrimSuffix(a, "/")+"/") {
			return true
		}
	}
	return false
}
