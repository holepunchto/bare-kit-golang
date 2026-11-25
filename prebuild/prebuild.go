// Package prebuild downloads the prebuilt Bare Kit libraries that the releases
// of this module provide.
package prebuild

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Version is the version of this module, whose release provides the
// prebuilds.
const Version = "0.1.0"

// The SHA-256 checksums of the prebuilds attached to the release of this
// version, which the publish workflow checks before releasing them.
var checksums = map[string]string{
	"android-arm":         "a186c89365ef51c4913cb05395ba7d456659962f07736c3da23a41473d4a1885",
	"android-arm64":       "8e4c363e71140fd131f0ad4adf510d915c25e88482573f72221e76412f5f4c8e",
	"android-ia32":        "8ef1cc9d0323a1b41ebc66283b061469544979837d4d008c779efe3184e23bdc",
	"android-x64":         "e17583816c469352c3b6e86b71d958d8a66ab2e0372e59d301fc08a848205131",
	"darwin-arm64":        "fbc9648ab5117ed6d4a44c1ec93a45f29c8f604916112384f865a43e70d9a7d0",
	"darwin-x64":          "bbaeb171c599ab6fc14c28dec1b3d5e0a1ee87afcc52e65ae69eb63f1f6c2190",
	"ios-arm64-simulator": "ab039ce312d6e7ac74bce52fe6a0df57f26124396009663ff1a7e1425952bebd",
	"ios-arm64":           "f6cfa414468ff70be78395694a0f2cd64e16d7c13ba15252f5fd8083b34c0258",
	"ios-x64-simulator":   "6eddeabdbaae88ca5fe88997582b2640575a88cbdc71dc583bccb550156f9046",
	"linux-arm64":         "3e858acbd418c64b2ec95d280c37765b98f1c3e9e591276633396db8d5308540",
	"linux-x64":           "aeb395ab1f96661d81062b412b754fa5316199fad6377c6177e6d066de9577c9",
	"win32-arm64":         "c1be7a1d80e4cd0df275df7bde3bca4683dc49ac4c39d5f32befa0bc5d80fa4f",
	"win32-x64":           "9e2172b5788aa127fe1bbb47c98a8c768c4cc7092c1e82d343ce96d1965892b3",
}

const mirror = "https://github.com/holepunchto/bare-kit-golang/releases/download"

// Options are the options of Download.
type Options struct {
	// Dir is the directory to download to, which defaults to a directory of
	// the version in the cache directory of the user.
	Dir string

	// Target is the target to download for, such as `darwin-arm64`, which
	// defaults to the running platform.
	Target string

	// Mirror is the URL to download releases from, which defaults to the
	// GitHub releases of this module.
	Mirror string
}

// Download downloads the prebuilt Bare Kit library of this version and
// extracts it, unless an earlier download already did, and returns its path.
func Download(ctx context.Context, opts *Options) (string, error) {
	if opts == nil {
		opts = &Options{}
	}

	target := opts.Target

	if target == "" {
		var err error

		target, err = Target(runtime.GOOS, runtime.GOARCH)
		if err != nil {
			return "", err
		}
	}

	checksum, ok := checksums[target]
	if !ok {
		return "", fmt.Errorf("no Bare Kit prebuild for %s", target)
	}

	dir := opts.Dir

	if dir == "" {
		cache, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}

		dir = filepath.Join(cache, "bare-kit", Version)
	}

	archive := filepath.Join(dir, target+".tar.gz")
	out := filepath.Join(dir, "prebuilds", target)
	library := filepath.Join(out, Library(target))

	if exists(library) && verify(archive, checksum) {
		return library, nil
	}

	base := opts.Mirror

	if base == "" {
		base = mirror
	}

	url := fmt.Sprintf("%s/v%s/%s.tar.gz", strings.TrimSuffix(base, "/"), Version, target)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}

	if err := download(ctx, url, archive, checksum); err != nil {
		return "", err
	}

	// Extracted aside and renamed into place, so that processes downloading at
	// the same time never run a partial copy.
	temporary, err := os.MkdirTemp(dir, "extract-")
	if err != nil {
		return "", err
	}

	defer os.RemoveAll(temporary)

	if err := extract(archive, temporary); err != nil {
		return "", err
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}

	os.RemoveAll(out)

	err = os.Rename(filepath.Join(temporary, "prebuilds", target), out)

	// Another process may have extracted it in the meantime, which Windows
	// refuses to replace while it is loaded.
	if err != nil && !exists(library) {
		return "", err
	}

	return library, nil
}

// Target returns the target named as by Bare, such as `darwin-arm64`, of an
// operating system and architecture named as by Go. As Go names no iOS
// simulator of its own, iOS on arm64 is taken to be a device, and the
// simulator target `ios-arm64-simulator` must be given explicitly.
func Target(goos, goarch string) (string, error) {
	targets := map[string]map[string]string{
		"darwin":  {"amd64": "x64", "arm64": "arm64"},
		"linux":   {"amd64": "x64", "arm64": "arm64"},
		"windows": {"amd64": "x64", "arm64": "arm64"},
		"android": {"amd64": "x64", "arm64": "arm64", "arm": "arm", "386": "ia32"},
		"ios":     {"amd64": "x64-simulator", "arm64": "arm64"},
	}

	archs, ok := targets[goos]
	if !ok {
		return "", fmt.Errorf("unsupported operating system %s", goos)
	}

	arch, ok := archs[goarch]
	if !ok {
		return "", fmt.Errorf("unsupported architecture %s", goarch)
	}

	platform := goos

	if goos == "windows" {
		platform = "win32"
	}

	return platform + "-" + arch, nil
}

// Library returns the file name of the Bare Kit library of a target.
func Library(target string) string {
	switch {
	case strings.HasPrefix(target, "darwin-"), strings.HasPrefix(target, "ios-"):
		return "libbare-kit.dylib"
	case strings.HasPrefix(target, "win32-"):
		return "bare-kit.dll"
	default:
		return "libbare-kit.so"
	}
}

// Written aside and renamed into place once verified, so that a failed
// download never replaces a verified archive.
func download(ctx context.Context, url, archive, checksum string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}

	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, res.Status)
	}

	file, err := os.CreateTemp(filepath.Dir(archive), "download-")
	if err != nil {
		return err
	}

	defer os.Remove(file.Name())

	sum, err := digest(io.TeeReader(res.Body, file))

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return err
	}

	if sum != checksum {
		return fmt.Errorf("checksum mismatch for %s", url)
	}

	return os.Rename(file.Name(), archive)
}

// Rooted at dir, so that no entry is written outside of it.
func extract(archive, dir string) error {
	file, err := os.Open(archive)
	if err != nil {
		return err
	}

	defer file.Close()

	gz, err := gzip.NewReader(file)
	if err != nil {
		return err
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}

	defer root.Close()

	reader := tar.NewReader(gz)

	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}

		if err != nil {
			return err
		}

		name := filepath.FromSlash(header.Name)

		switch header.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(name, 0o755)
		case tar.TypeReg:
			err = extractFile(root, name, header.FileInfo().Mode().Perm(), reader)
		}

		if err != nil {
			return err
		}
	}
}

func extractFile(root *os.Root, name string, perm os.FileMode, data io.Reader) error {
	if err := root.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return err
	}

	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}

	_, err = io.Copy(file, data)

	if closeErr := file.Close(); err == nil {
		err = closeErr
	}

	return err
}

func verify(archive, checksum string) bool {
	file, err := os.Open(archive)
	if err != nil {
		return false
	}

	defer file.Close()

	sum, err := digest(file)

	return err == nil && sum == checksum
}

func digest(r io.Reader) (string, error) {
	hash := sha256.New()

	if _, err := io.Copy(hash, r); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}

func exists(path string) bool {
	_, err := os.Stat(path)

	return err == nil
}
