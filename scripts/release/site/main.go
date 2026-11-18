// Command site writes the Kiko download and documentation page published
// to the repository's GitHub Pages: one page with a platform picker
// whose install, upgrade and uninstall commands copy with a click.
//
//	go run ./scripts/release/site -repo owner/kiko -version 0.3.0 -o build/site
package main

import (
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"os"
	"path/filepath"
	"regexp"
)

//go:embed index.html.tmpl
var indexTemplate string

var page = template.Must(template.New("index").Parse(indexTemplate))

var (
	repoPattern    = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)
	versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$`)
)

// Site is what the page shows: the repository the releases are in and
// the version it was generated for.
type Site struct {
	Repo    string
	Version string
}

func (s Site) validate() error {
	if !repoPattern.MatchString(s.Repo) {
		return fmt.Errorf("repository must be owner/name: %q", s.Repo)
	}
	if !versionPattern.MatchString(s.Version) {
		return fmt.Errorf("version must be x.y.z: %q", s.Version)
	}
	return nil
}

// Latest is the directory of the latest release's files, which the
// install scripts and the upgrade commands read.
func (s Site) Latest() string {
	return "https://github.com/" + s.Repo + "/releases/latest/download"
}

// Download is the URL of one file of this version's release.
func (s Site) Download(name string) string {
	return "https://github.com/" + s.Repo + "/releases/download/v" + s.Version + "/" + name
}

func (s Site) Releases() string { return "https://github.com/" + s.Repo + "/releases" }

// The names mygo build gives the installers, after the app's slug.
func (s Site) DMG(arch string) string { return s.Download("kiko-" + s.Version + "-" + arch + ".dmg") }
func (s Site) Installer(arch string) string {
	return s.Download("kiko-setup-" + s.Version + "-" + arch + ".exe")
}
func (s Site) ServerArchive(arch string) string {
	return s.Download("kiko-server-linux-" + arch + ".tar.gz")
}

func render(w io.Writer, s Site) error {
	if err := s.validate(); err != nil {
		return err
	}
	return page.Execute(w, s)
}

func main() {
	var s Site
	var out string
	flag.StringVar(&s.Repo, "repo", "", "repository that hosts the releases, as owner/name")
	flag.StringVar(&s.Version, "version", "", "version, such as 0.3.0")
	flag.StringVar(&out, "o", "build/site", "output directory")
	flag.Parse()
	if err := write(out, s); err != nil {
		log.Fatal(err)
	}
}

func write(out string, s Site) error {
	if out == "" {
		return errors.New("missing output directory")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(out, "index.html"))
	if err != nil {
		return err
	}
	if err := render(f, s); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	// GitHub Pages serves the files as they are, without Jekyll.
	return os.WriteFile(filepath.Join(out, ".nojekyll"), nil, 0o644)
}
