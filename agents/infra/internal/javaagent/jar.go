package javaagent

import (
	"archive/zip"
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const maxJarManifestBytes = 256 << 10

// ReadJarManifest returns the main attributes of META-INF/MANIFEST.MF (continuation lines joined).
func ReadJarManifest(path string) (map[string]string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("%s is not a jar: %w", path, err)
	}
	defer zr.Close()
	for _, f := range zr.File {
		if !strings.EqualFold(f.Name, "META-INF/MANIFEST.MF") {
			continue
		}
		if f.UncompressedSize64 > maxJarManifestBytes {
			return nil, fmt.Errorf("%s: MANIFEST.MF too large", path)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, maxJarManifestBytes+1))
		rc.Close()
		if err != nil {
			return nil, err
		}
		return parseManifestMF(b), nil
	}
	return nil, fmt.Errorf("%s has no META-INF/MANIFEST.MF", path)
}

// parseManifestMF parses the main section of a jar manifest.
func parseManifestMF(b []byte) map[string]string {
	attrs := map[string]string{}
	var last string
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 4096), maxJarManifestBytes)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			break // end of the main section
		}
		if strings.HasPrefix(line, " ") && last != "" {
			attrs[last] += line[1:]
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		last = strings.TrimSpace(k)
		attrs[last] = strings.TrimSpace(v)
	}
	return attrs
}

// ValidateJar checks that path is a Java agent jar (Premain-Class) and, when the jar names its openlog version,
// that it is version.
func ValidateJar(path, version string) error {
	attrs, err := ReadJarManifest(path)
	if err != nil {
		return err
	}
	if attrs["Premain-Class"] == "" {
		return fmt.Errorf("%s is not a Java agent (no Premain-Class)", path)
	}
	if v := attrs[VersionAttribute]; version != "" && v != "" && v != version {
		return fmt.Errorf("jar version %s does not match release %s", v, version)
	}
	return nil
}

// JarVersion is the openlog version a jar names ("" when unreadable or not an openlog jar).
func JarVersion(path string) string {
	attrs, err := ReadJarManifest(path)
	if err != nil {
		return ""
	}
	return attrs[VersionAttribute]
}

// fileSHA256 returns the hex sha256 of a regular file of at most MaxJarBytes.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxJarBytes {
		return "", errors.New(path + " is not a regular file of a jar's size")
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
