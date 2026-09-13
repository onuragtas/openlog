package rpmdb

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Package is one installed rpm.
type Package struct {
	Name    string
	Version string // [epoch:]version-release, like dpkg's epoch:upstream-revision
	Arch    string
}

// rpm header tags and types.
const (
	tagName    = 1000
	tagVersion = 1001
	tagRelease = 1002
	tagEpoch   = 1003
	tagArch    = 1022

	typeInt32       = 4
	typeString      = 6
	typeStringArray = 8
	typeI18NString  = 9
)

// ReadSQLite reads installed packages from an rpmdb.sqlite file (RHEL/Rocky/
// Alma 9+, Fedora 33+). Committed transactions still in the -wal file are included.
func ReadSQLite(file string) ([]Package, error) {
	db, err := openSQLite(file)
	if err != nil {
		return nil, err
	}
	return readPackages(db)
}

func readPackages(db *sqliteDB) ([]Package, error) {
	root, err := db.tableRoot("Packages")
	if err != nil {
		return nil, err
	}
	var out []Package
	err = db.scanTable(root, func(_ int64, payload []byte) error {
		cols, err := decodeRecord(payload)
		if err != nil {
			return err
		}
		// CREATE TABLE Packages (hnum INTEGER PRIMARY KEY AUTOINCREMENT, blob BLOB NOT NULL)
		if len(cols) < 2 || cols[1].b == nil {
			return nil
		}
		p, err := ParseHeader(cols[1].b)
		if err != nil {
			return nil // skip one bad header rather than the whole database
		}
		if p.Name != "" && p.Name != "gpg-pubkey" {
			out = append(out, p)
		}
		return nil
	})
	return out, err
}

// ParseHeader parses an rpm header blob as stored in the rpm database
// (index count, data length, index entries, data store; no magic/lead).
func ParseHeader(blob []byte) (Package, error) {
	if len(blob) < 8 {
		return Package{}, errors.New("rpmdb: short header")
	}
	il := int(binary.BigEndian.Uint32(blob[0:4]))
	dl := int(binary.BigEndian.Uint32(blob[4:8]))
	if il <= 0 || il > 1<<16 || dl < 0 || 8+16*il+dl > len(blob) {
		return Package{}, errors.New("rpmdb: bad header sizes")
	}
	store := blob[8+16*il : 8+16*il+dl]
	var p Package
	var release string
	epoch := -1
	str := func(off int) string {
		if off < 0 || off >= len(store) {
			return ""
		}
		end := bytes.IndexByte(store[off:], 0)
		if end < 0 {
			return ""
		}
		return string(store[off : off+end])
	}
	for i := 0; i < il; i++ {
		e := blob[8+16*i : 8+16*(i+1)]
		tag := binary.BigEndian.Uint32(e[0:4])
		typ := binary.BigEndian.Uint32(e[4:8])
		off := int(int32(binary.BigEndian.Uint32(e[8:12])))
		switch tag {
		case tagName, tagVersion, tagRelease, tagArch:
			if typ != typeString && typ != typeI18NString && typ != typeStringArray {
				continue
			}
			v := str(off)
			switch tag {
			case tagName:
				p.Name = v
			case tagVersion:
				p.Version = v
			case tagRelease:
				release = v
			case tagArch:
				p.Arch = v
			}
		case tagEpoch:
			if typ == typeInt32 && off >= 0 && off+4 <= len(store) {
				epoch = int(int32(binary.BigEndian.Uint32(store[off:])))
			}
		}
	}
	p.Version = formatEVR(epoch, p.Version, release)
	return p, nil
}

func formatEVR(epoch int, version, release string) string {
	v := version
	if release != "" {
		v += "-" + release
	}
	if epoch > 0 {
		v = strconv.Itoa(epoch) + ":" + v
	}
	return v
}

// QueryFormat is the rpm --queryformat used by ReadCLI.
const QueryFormat = `%{NAME}\t%|EPOCH?{%{EPOCH}:}:{}|%{VERSION}-%{RELEASE}\t%{ARCH}\n`

// ReadCLI runs "rpm -qa" (for Berkeley DB / ndb databases on older
// distributions). root is passed as --root when it is not "/".
func ReadCLI(ctx context.Context, rpmPath, root string) ([]Package, error) {
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := []string{"-qa", "--queryformat", QueryFormat}
	if root != "" && root != "/" {
		args = append([]string{"--root", root}, args...)
	}
	out, err := exec.CommandContext(ctx, rpmPath, args...).Output()
	if err != nil {
		return nil, err
	}
	return ParseQueryOutput(out), nil
}

// ParseQueryOutput parses lines produced with QueryFormat.
func ParseQueryOutput(out []byte) []Package {
	var pkgs []Package
	for line := range strings.Lines(string(out)) {
		f := strings.Split(strings.TrimRight(line, "\n"), "\t")
		if len(f) != 3 || f[0] == "" || f[0] == "gpg-pubkey" {
			continue
		}
		arch := f[2]
		if arch == "(none)" {
			arch = ""
		}
		pkgs = append(pkgs, Package{Name: f[0], Version: f[1], Arch: arch})
	}
	return pkgs
}
