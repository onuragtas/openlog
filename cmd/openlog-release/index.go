package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	lib "github.com/onuragtas/openlog/libs/release"
)

type indexItem struct {
	version     lib.Version
	channel     string
	manifestURL string
}

func cmdBuildIndex(args []string, stdout io.Writer) error {
	fs := newFlagSet("build-index")
	out := fs.String("out", "", "output index.json")
	baseURL := fs.String("base-url", "", "releases root URL; a manifest file's URL becomes ROOT/v<version>/manifest.json")
	merge := fs.String("merge", "", "existing index.json whose entries are kept (unless overridden)")
	keysSpec := fs.String("keys", "", "if set, every MANIFEST must verify against MANIFEST.sig with these keys")
	generatedAt := fs.String("generated-at", "", "RFC 3339 generation time (default $SOURCE_DATE_EPOCH or now)")
	var entries multiFlag
	fs.Var(&entries, "entry", "version=manifest_url of an existing release; channel follows the version; repeatable")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *out == "" {
		return usagef("--out is required")
	}
	t, err := releaseTime(*generatedAt)
	if err != nil {
		return err
	}

	items := map[string]indexItem{}
	add := func(it indexItem) { items[it.version.String()] = it }

	if *merge != "" {
		data, err := os.ReadFile(*merge)
		if err != nil {
			return err
		}
		old, err := lib.ParseIndex(data)
		if err != nil {
			return err
		}
		for ch, list := range old.Channels {
			for _, e := range list {
				add(indexItem{lib.MustParseVersion(e.Version), ch, e.ManifestURL})
			}
		}
	}
	for _, e := range entries {
		v, u, ok := strings.Cut(e, "=")
		if !ok {
			return usagef("--entry %q: want version=manifest_url", e)
		}
		pv, err := lib.ParseVersion(v)
		if err != nil {
			return err
		}
		add(indexItem{pv, channelOf(pv), u})
	}
	for _, path := range fs.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var m *lib.Manifest
		if *keysSpec != "" {
			keys, err := loadKeys(*keysSpec)
			if err != nil {
				return err
			}
			sig, err := os.ReadFile(path + ".sig")
			if err != nil {
				return err
			}
			if m, _, err = lib.VerifyManifest(data, sig, keys); err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
		} else if m, err = lib.ParseManifest(data); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if *baseURL == "" {
			return usagef("--base-url is required when manifests are given")
		}
		u := strings.TrimRight(*baseURL, "/") + "/v" + m.Version + "/manifest.json"
		add(indexItem{m.ParsedVersion(), m.Channel, u})
	}

	idx := buildIndex(items, t)
	data, err := marshalJSON(idx)
	if err != nil {
		return err
	}
	if _, err := lib.ParseIndex(data); err != nil {
		return fmt.Errorf("generated index is invalid: %w", err)
	}
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s: stable=%d beta=%d\n", *out, len(idx.Channels[lib.ChannelStable]), len(idx.Channels[lib.ChannelBeta]))
	return nil
}

func channelOf(v lib.Version) string {
	if v.IsPrerelease() {
		return lib.ChannelBeta
	}
	return lib.ChannelStable
}

// buildIndex sorts releases newest first per channel. Pre-releases can never be on stable; plain
// versions published to beta stay on beta (clients on beta also see stable, see Index.Latest).
func buildIndex(items map[string]indexItem, t time.Time) *lib.Index {
	idx := &lib.Index{
		Schema:      lib.SchemaVersion,
		Product:     lib.Product,
		GeneratedAt: t.UTC(),
		Channels:    map[string][]lib.IndexEntry{lib.ChannelStable: {}, lib.ChannelBeta: {}},
	}
	list := make([]indexItem, 0, len(items))
	for _, it := range items {
		if it.version.IsPrerelease() || it.channel != lib.ChannelStable {
			it.channel = lib.ChannelBeta
		}
		list = append(list, it)
	}
	sort.Slice(list, func(i, j int) bool { return lib.Compare(list[i].version, list[j].version) > 0 })
	for _, it := range list {
		idx.Channels[it.channel] = append(idx.Channels[it.channel], lib.IndexEntry{Version: it.version.String(), ManifestURL: it.manifestURL})
	}
	return idx
}
