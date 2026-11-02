// Command update regenerates the vendored native source subsets and cgo wrappers.
package main

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type dependency struct {
	name       string
	repository string
	revision   string
}

var dependencies = []dependency{
	{"turbo", "libjpeg-turbo/libjpeg-turbo", "7b4993786d856cbccabfc0ed2f923ba000e326cc"},
	{"simde", "simd-everywhere/simde", "71fd833d9666141edcd1d3c109a80e228303d8d7"},
	{"openjpeg", "uclouvain/openjpeg", "6c4a29b00211eb0430fa0e5e890f1ce5c80f409f"},
	{"charls", "team-charls/charls", "36dd3307e070d8fbc765c3ba890b7e681046fa39"},
	{"libjxl", "libjxl/libjxl", "794a5dcf0d54f9f0b20d288a12e87afb91d20dfc"},
	{"highway", "google/highway", "457c891775a7397bdb0376bb1031e6e027af1c48"},
	{"brotli", "google/brotli", "36533a866ed1ca4b75cf049f4521e4ec5fe24727"},
	{"lcms", "mm2/Little-CMS", "5176347635785e53ee5cee92328f76fda766ecc6"},
}

func main() {
	for _, dep := range dependencies {
		checkout(dep)
	}

	packages := []string{"classic", "jpeg2000", "jpegls", "jpegxl"}

	for _, name := range packages {
		must(os.MkdirAll(name, 0o755))

		files, err := filepath.Glob(filepath.Join(name, "z_*"))
		must(err)

		for _, file := range files {
			must(os.Remove(file))
		}
	}

	// Only generated trees are replaced; the source checkouts remain available offline.
	must(os.RemoveAll("internal/native"))

	generateTurbo()
	generateOpenJPEG()
	generateCharLS()
	generateJXL()

	var manifest strings.Builder

	for _, dep := range dependencies {
		fmt.Fprintf(&manifest, "%s https://github.com/%s %s\n", dep.name, dep.repository, dep.revision)
	}

	write("internal/native/REVISIONS", manifest.String())
}

func checkout(dep dependency) {
	directory := filepath.Join(".cache", dep.name)

	_, err := os.Stat(filepath.Join(directory, ".git"))
	if os.IsNotExist(err) {
		must(os.MkdirAll(".cache", 0o755))

		run("", "git", "clone", "--no-checkout", "https://github.com/"+dep.repository+".git", directory)
	} else {
		must(err)
	}

	command := exec.Command("git", "cat-file", "-e", dep.revision+"^{commit}")
	command.Dir = directory

	err = command.Run()
	if err != nil {
		run(directory, "git", "fetch", "origin", dep.revision)
	}

	run(directory, "git", "checkout", "--detach", dep.revision)

	command = exec.Command("git", "status", "--porcelain", "--untracked-files=no")
	command.Dir = directory

	changes, err := command.Output()
	must(err)

	if len(changes) != 0 {
		panic("refusing modified upstream checkout: " + directory)
	}
}

func generateTurbo() {
	sources := cmakeList(".cache/turbo/CMakeLists.txt", "JPEG_SOURCES")
	sources = append(sources, "src/jaricom.c", "src/jcarith.c", "src/jdarith.c")

	copyTree("turbo", "src", func(path string) bool {
		return !strings.Contains(path, "/") && strings.HasSuffix(path, ".h")
	})

	copyTree("turbo", "simd", func(path string) bool {
		return strings.HasSuffix(path, ".h") || strings.HasPrefix(path, "arm/") && strings.HasSuffix(path, ".c") || path == "jsimd.c"
	})

	copyTree("simde", "simde", func(path string) bool {
		return strings.HasSuffix(path, ".h")
	})

	licenses("turbo", "LICENSE.md", "README.ijg")
	licenses("simde", "COPYING")

	for _, source := range sources {
		copyTranslationUnit("turbo", source)
		wrapper("classic", "turbo", source, "")
	}

	simd := []string{"jccolor", "jcgray", "aarch64/jchuff", "jcphuff", "jcsample", "jdcolor", "jdmerge", "jdsample", "jfdctfst", "jfdctint", "jidctfst", "jidctint", "jidctred", "jquanti"}

	for _, source := range simd {
		wrapper("classic", "turbo", "simd/arm/"+source+"-neon.c", "")
	}

	wrapper("classic", "turbo", "simd/arm/aarch64/jsimdcpu.c", "")
	wrapper("classic", "turbo", "simd/jsimd.c", "")

	write("internal/native/turbo/src/jconfig.h", turboConfig)
	write("internal/native/turbo/src/jconfigint.h", turboInternalConfig)

	compat := read(".cache/turbo/simd/arm/neon-compat.h.in")
	compat = strings.ReplaceAll(compat, "#cmakedefine", "#define")
	compat = patch(compat, "#define SIMDE_ENABLE_NATIVE_ALIASES", "/* coalaura: only the ARM aliases are needed; leave x86 intrinsics untouched. */\n#define SIMDE_ARM_NEON_A32V7_ENABLE_NATIVE_ALIASES\n#define SIMDE_ARM_NEON_A32V8_ENABLE_NATIVE_ALIASES\n#define SIMDE_ARM_NEON_A64V8_ENABLE_NATIVE_ALIASES")

	write("internal/native/turbo/simd/arm/neon-compat.h", compat)
	write("internal/native/turbo/src/jversion.h", strings.ReplaceAll(read(".cache/turbo/src/jversion.h.in"), "@COPYRIGHT_YEAR@", "2026"))
}

func generateOpenJPEG() {
	sources := []string{"thread", "bio", "cio", "dwt", "event", "ht_dec", "image", "invert", "j2k", "jp2", "mct", "mqc", "openjpeg", "opj_clock", "pi", "t1", "t2", "tcd", "tgt", "function_list", "opj_malloc", "sparse_array"}

	copyTree("openjpeg", "src/lib/openjp2", func(path string) bool {
		return strings.HasSuffix(path, ".h") || slices.Contains(sources, strings.TrimSuffix(path, ".c"))
	})

	licenses("openjpeg", "LICENSE", "AUTHORS.md")

	for _, source := range sources {
		prefix := ""

		if source == "thread" {
			prefix = "#include \"../internal/native/openjpeg/src/lib/openjp2/opj_config_private.h\"\n"
		}

		wrapper("jpeg2000", "openjpeg", "src/lib/openjp2/"+source+".c", prefix)
	}

	j2k := read("internal/native/openjpeg/src/lib/openjp2/j2k.c")
	j2k = patch(j2k, "const char* num_threads_str = getenv(\"OPJ_NUM_THREADS\");", "/* coalaura: the bridge sets a bounded worker count explicitly. */\n    const char* num_threads_str = NULL;")

	write("internal/native/openjpeg/src/lib/openjp2/j2k.c", j2k)
	write("internal/native/openjpeg/src/lib/openjp2/opj_config.h", "#define OPJ_VERSION_MAJOR 2\n#define OPJ_VERSION_MINOR 5\n#define OPJ_VERSION_BUILD 4\n")
	write("internal/native/openjpeg/src/lib/openjp2/opj_config_private.h", openjpegConfig)
}

func generateCharLS() {
	copyTree("charls", "include", func(path string) bool {
		return strings.HasSuffix(path, ".h")
	})

	copyTree("charls", "src", func(path string) bool {
		return strings.HasSuffix(path, ".h") || strings.HasSuffix(path, ".cpp")
	})

	licenses("charls", "LICENSE.md")

	files, err := filepath.Glob("internal/native/charls/src/*.cpp")
	must(err)

	for _, file := range files {
		wrapper("jpegls", "charls", "src/"+filepath.Base(file), "")
	}
}

func generateJXL() {
	groups := []string{"BASE", "CMS", "DEC_BOX", "DEC_JPEG", "DEC", "ENC", "THREADS", "PUBLIC_HEADERS", "THREADS_PUBLIC_HEADERS"}
	seen := make(map[string]bool)

	for _, group := range groups {
		name := "JPEGXL_INTERNAL_" + group

		if !strings.HasSuffix(group, "HEADERS") {
			name += "_SOURCES"
		}

		for _, source := range cmakeList(".cache/libjxl/lib/jxl_lists.cmake", name) {
			if seen[source] {
				continue
			}

			seen[source] = true

			copyFile("libjxl", "lib/"+source)

			if strings.HasSuffix(source, ".cc") {
				wrapper("jpegxl", "libjxl", "lib/"+source, "")
			}
		}
	}

	copyTree("libjxl", "lib/include", func(path string) bool {
		return strings.HasSuffix(path, ".h")
	})

	copyTree("highway", "hwy", func(path string) bool {
		return strings.HasSuffix(path, ".h") && !strings.Contains(path, "tests/")
	})

	highway := []string{"abort", "aligned_allocator", "per_target", "print", "targets", "timer"}

	for _, source := range highway {
		copyFile("highway", "hwy/"+source+".cc")
		wrapper("jpegxl", "highway", "hwy/"+source+".cc", "")
	}

	copyTree("brotli", "c", func(path string) bool {
		codec := strings.HasPrefix(path, "common/") || strings.HasPrefix(path, "dec/") || strings.HasPrefix(path, "enc/") || strings.HasPrefix(path, "include/")
		return codec && (strings.HasSuffix(path, ".h") || strings.HasSuffix(path, ".c") || strings.HasSuffix(path, ".inc"))
	})

	copyTree("lcms", "include", func(path string) bool {
		return strings.HasSuffix(path, ".h")
	})

	copyTree("lcms", "src", func(path string) bool {
		return strings.HasSuffix(path, ".h") || strings.HasSuffix(path, ".c")
	})

	cDependencies := []string{"brotli", "lcms"}

	for _, dep := range cDependencies {
		must(filepath.WalkDir("internal/native/"+dep, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}

			if !entry.IsDir() && strings.HasSuffix(path, ".c") {
				source, err := filepath.Rel("internal/native/"+dep, path)
				must(err)

				wrapper("jpegxl", dep, filepath.ToSlash(source), "")
			}

			return nil
		}))
	}

	licenses("libjxl", "LICENSE", "PATENTS", "AUTHORS")
	licenses("highway", "LICENSE", "LICENSE-BSD3")
	licenses("brotli", "LICENSE")
	licenses("lcms", "LICENSE", "AUTHORS")

	exports := []string{"JXL", "JXL_CMS", "JXL_THREADS"}

	for _, component := range exports {
		write("internal/native/libjxl/lib/include/jxl/"+strings.ToLower(component)+"_export.h", "#pragma once\n#define "+component+"_EXPORT\n#define "+component+"_NO_EXPORT\n#define "+component+"_DEPRECATED __attribute__((deprecated))\n#define "+component+"_DEPRECATED_EXPORT "+component+"_DEPRECATED\n#define "+component+"_DEPRECATED_NO_EXPORT "+component+"_DEPRECATED\n")
	}

	write("internal/native/libjxl/lib/include/jxl/version.h", "#pragma once\n#define JPEGXL_MAJOR_VERSION 0\n#define JPEGXL_MINOR_VERSION 11\n#define JPEGXL_PATCH_VERSION 1\n#define JPEGXL_COMPUTE_VERSION(major, minor, patch) ((major) * 1000000 + (minor) * 1000 + (patch))\n#define JPEGXL_NUMERIC_VERSION JPEGXL_COMPUTE_VERSION(0, 11, 1)\n")
}

func wrapper(pkg, dep, source, prefix string) {
	extension := filepath.Ext(source)
	if extension == ".cc" {
		extension = ".cpp"
	}

	name := strings.NewReplacer("/", "_", "-", "_").Replace(strings.TrimSuffix(source, filepath.Ext(source)))
	write(pkg+"/z_"+dep+"_"+name+extension, "/* Code generated by internal/update. DO NOT EDIT. */\n"+prefix+"#include \"../internal/native/"+dep+"/"+source+"\"\n")
}

func cmakeList(path, name string) []string {
	pattern := regexp.MustCompile(`(?s)set\(` + regexp.QuoteMeta(name) + `\s+([^)]*)\)`)

	match := pattern.FindStringSubmatch(read(path))
	if len(match) != 2 {
		panic("missing source list: " + name)
	}

	return strings.Fields(match[1])
}

func copyTree(dep, subtree string, include func(string) bool) {
	root := filepath.Join(".cache", dep, subtree)

	must(filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if entry.IsDir() {
			return nil
		}

		relative, err := filepath.Rel(root, path)
		must(err)

		relative = filepath.ToSlash(relative)
		if include(relative) {
			copyFile(dep, subtree+"/"+relative)
		}

		return nil
	}))
}

func licenses(dep string, paths ...string) {
	for _, path := range paths {
		copyFile(dep, path)
	}
}

func copyFile(dep, path string) {
	write("internal/native/"+dep+"/"+path, read(".cache/"+dep+"/"+path))
}

// Multi-precision libjpeg wrappers include shared C implementations as templates.
func copyTranslationUnit(dep, path string) {
	data := read(".cache/" + dep + "/" + path)

	write("internal/native/"+dep+"/"+path, data)

	pattern := regexp.MustCompile(`#include "([^"]+\.c)"`)

	for _, match := range pattern.FindAllStringSubmatch(data, -1) {
		included := filepath.ToSlash(filepath.Join(filepath.Dir(path), match[1]))
		copyTranslationUnit(dep, included)
	}
}

func read(path string) string {
	data, err := os.ReadFile(path)
	must(err)

	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

func write(path, data string) {
	must(os.MkdirAll(filepath.Dir(path), 0o755))
	must(os.WriteFile(path, []byte(data), 0o644))
}

func run(directory, program string, args ...string) {
	command := exec.Command(program, args...)
	command.Dir = directory
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr

	must(command.Run())
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func patch(data, before, after string) string {
	if strings.Count(data, before) != 1 {
		panic("patch anchor mismatch: " + before)
	}

	return strings.Replace(data, before, after, 1)
}
