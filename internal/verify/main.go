// Command verify packages a local module zip and links offline external consumers.
package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const module = "github.com/coalaura/jpeg"

func main() {
	targets := flag.String("targets", "windows/amd64,windows/arm64,linux/amd64,linux/arm64,darwin/amd64,darwin/arm64", "comma-separated OS/arch pairs")
	flag.Parse()

	root, err := os.Getwd()
	must(err)

	proxy := filepath.Join(root, ".cache", "proxy")
	base := filepath.Join(proxy, filepath.FromSlash(module), "@v")

	must(os.MkdirAll(base, 0o755))

	moduleFile, err := os.ReadFile("go.mod")
	must(err)

	files, version := moduleFiles()

	write(filepath.Join(base, version+".mod"), moduleFile)
	write(filepath.Join(base, version+".info"), fmt.Appendf(nil, `{"Version":%q,"Time":"2026-01-01T00:00:00Z"}`, version))
	write(filepath.Join(base, "list"), []byte(version+"\n"))

	var archive bytes.Buffer

	writer := zip.NewWriter(&archive)

	for _, path := range files {
		data, readError := os.ReadFile(path)
		must(readError)

		target, createError := writer.Create(module + "@" + version + "/" + filepath.ToSlash(path))
		must(createError)

		_, writeError := target.Write(data)
		must(writeError)
	}

	must(writer.Close())

	write(filepath.Join(base, version+".zip"), archive.Bytes())

	consumer := filepath.Join(root, ".cache", "consumer")
	must(os.MkdirAll(consumer, 0o755))

	write(filepath.Join(consumer, "go.mod"), []byte("module consumer\n\ngo 1.27.3\n\nrequire "+module+" "+version+"\n"))

	err = os.Remove(filepath.Join(consumer, "go.sum"))
	if err != nil && !os.IsNotExist(err) {
		must(err)
	}

	packages := []string{"root", "classic", "jpeg2000", "jpegls", "jpegxl"}

	for _, name := range packages {
		path := module

		if name != "root" {
			path += "/" + name
		}

		directory := filepath.Join(consumer, name)
		must(os.MkdirAll(directory, 0o755))

		source := fmt.Sprintf("package main\nimport (\"bytes\"; \"image\"; codec %q)\nfunc main() { var buffer bytes.Buffer; if err := codec.Encode(&buffer, image.NewGray(image.Rect(0,0,8,8)), nil); err != nil { panic(err) }; if _, err := codec.Decode(&buffer); err != nil { panic(err) } }\n", path)
		write(filepath.Join(directory, "main.go"), []byte(source))
	}

	cache := filepath.Join(root, ".cache", "modulecache")

	// This cache is exclusively owned by this verification command. Remove its
	// previous download so each run exercises the newly created module zip.
	err = filepath.WalkDir(cache, func(path string, entry fs.DirEntry, walkError error) error {
		if walkError == nil {
			return os.Chmod(path, 0o755)
		}

		return nil
	})

	must(err)
	must(os.RemoveAll(cache))

	proxyPath := filepath.ToSlash(proxy)
	if !strings.HasPrefix(proxyPath, "/") {
		proxyPath = "/" + proxyPath
	}

	proxyURL := url.URL{Scheme: "file", Path: proxyPath}

	environment := os.Environ()
	environment = append(environment, "GOPROXY="+proxyURL.String(), "GOSUMDB=off", "GOMODCACHE="+cache, "GOTOOLCHAIN=local", "CGO_ENABLED=1")

	run(consumer, environment, "go", "mod", "download", module)

	for _, name := range packages[1:] {
		command := exec.Command("go", "list", "-deps", "./"+name)
		command.Dir = consumer
		command.Env = environment

		output, listError := command.Output()
		must(listError)

		for _, other := range packages[1:] {
			if name != other && bytes.Contains(output, []byte(module+"/"+other+"\n")) {
				panic("non-independent codec import: " + name + " -> " + other)
			}
		}
	}

	for target := range strings.SplitSeq(*targets, ",") {
		operatingSystem, architecture, ok := strings.Cut(target, "/")
		if !ok {
			panic("invalid target: " + target)
		}

		for _, name := range packages {
			output := filepath.Join(consumer, operatingSystem+"-"+architecture+"-"+name)
			arguments := []string{"build", "go", operatingSystem, "--arch", architecture, "--cgo", "--compat", "--no-gen", "--pkg", "./" + name, "--output", output}

			if runtime.GOOS == "windows" && operatingSystem == "darwin" {
				// Go's Mach-O UUID rewriter renames a still-open file, which fails on
				// Windows. Omitting the Go build ID avoids that host-toolchain bug
				// without -B none, which asks Zig for the unsupported -no_uuid flag.
				arguments = append(arguments, "-ldflags", "-buildid=")
			}

			run(consumer, environment, "builder", arguments...)

			fmt.Println("PASS", target, name)
		}
	}
}

func moduleFiles() ([]string, string) {
	files := make([]string, 0, 2048)
	digest := sha256.New()

	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, walkError error) error {
		if walkError != nil {
			return walkError
		}

		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".cache" {
				return filepath.SkipDir
			}

			return nil
		}

		if !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular module file %s", path)
		}

		if strings.HasSuffix(path, ".exe") {
			return nil
		}

		files = append(files, path)

		fmt.Fprintf(digest, "%s\x00", filepath.ToSlash(path))

		source, openError := os.Open(path)
		if openError != nil {
			return openError
		}

		_, copyError := io.Copy(digest, source)
		closeError := source.Close()

		if copyError != nil {
			return copyError
		}

		return closeError
	})

	must(err)

	// Module source indexes assume immutable versions, even when the download
	// cache is removed. A content-derived version prevents stale import indexes.
	version := fmt.Sprintf("v0.0.0-20260101000000-%x", digest.Sum(nil)[:6])
	return files, version
}

func write(path string, data []byte) {
	must(os.WriteFile(path, data, 0o644))
}

func run(directory string, environment []string, program string, arguments ...string) {
	command := exec.Command(program, arguments...)
	command.Dir = directory
	command.Env = environment
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	must(command.Run())
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
