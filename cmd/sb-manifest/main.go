// Command sb-manifest writes an update manifest for a release, after checking
// every signature against the release public key. The release workflow runs
// it; see docs/updates.md.
//
//	sb-manifest -version v0.2.0 -key RW... -base-url https://.../download/v0.2.0 dist/sb_*.tar.gz dist/sb_*.zip
//	sb-manifest -tray -version v0.2.0 -key RW... -base-url ... Switchboard.app.tar.gz Switchboard_0.2.0_x64-setup.exe
//
// With -verify CHANNEL it instead checks a signed manifest, FILE and
// FILE.minisig, exactly as sb self-update (or, with -tray, the tray app) will:
//
//	sb-manifest -verify stable -key RW... updates/stable.json
//	sb-manifest -tray -verify stable -key RW... updates/tray/stable.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/nanaaikinson/switchboard/internal/update"
)

func main() {
	var o update.PublishOptions
	tray := flag.Bool("tray", false, "build the tray app's Tauri updater manifest instead of sb's")
	notesFile := flag.String("notes-file", "", "file with release notes")
	flag.StringVar(&o.Version, "version", "", "release version, e.g. v0.2.0")
	flag.StringVar(&o.PublicKey, "key", os.Getenv("SB_UPDATE_PUBLIC_KEY"), "release minisign public key (default $SB_UPDATE_PUBLIC_KEY)")
	flag.StringVar(&o.BaseURL, "base-url", "", "URL the files are downloaded from")
	flag.IntVar(&o.Rollout, "rollout", 100, "rollout_percent, 0-100")
	verify := flag.String("verify", "", "check FILE and FILE.minisig as the signed manifest of this channel, instead of building one")
	flag.Parse()
	if *verify != "" {
		if flag.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: sb-manifest [-tray] -verify CHANNEL -key RW... FILE")
			os.Exit(2)
		}
		var version string
		if *tray {
			m, err := update.CheckSignedTrayManifest(o.PublicKey, *verify, flag.Arg(0))
			if err != nil {
				fail(err)
			}
			version = m.Version
		} else {
			m, err := update.CheckSignedManifest(o.PublicKey, *verify, flag.Arg(0))
			if err != nil {
				fail(err)
			}
			version = m.Version
		}
		fmt.Fprintf(os.Stderr, "sb-manifest: %s: %s on %s, signed\n", flag.Arg(0), version, *verify)
		return
	}
	if o.Version == "" || o.BaseURL == "" || flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "usage: sb-manifest [-tray] -version vX.Y.Z -key RW... -base-url URL FILE...")
		os.Exit(2)
	}
	if *notesFile != "" {
		b, err := os.ReadFile(*notesFile)
		if err != nil {
			fail(err)
		}
		o.Notes = string(b)
	}
	o.PubDate = time.Now().UTC().Format(time.RFC3339)
	var out any
	var err error
	if *tray {
		out, err = update.BuildTrayManifest(o, flag.Args())
	} else {
		out, err = update.BuildManifest(o, flag.Args())
	}
	if err != nil {
		fail(err)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		fail(err)
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sb-manifest:", err)
	os.Exit(1)
}
