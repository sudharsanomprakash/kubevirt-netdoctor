// Package bundle writes a self-describing diagnostic bundle: a gzip'd tar
// archive with a manifest listing every file and its SHA-256.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"sort"
	"time"
)

// SchemaVersion versions the manifest.
const SchemaVersion = "netdoctor.bundle/v1alpha1"

// Manifest describes a bundle.
type Manifest struct {
	SchemaVersion string `json:"schemaVersion"`
	ToolVersion   string `json:"toolVersion"`
	CreatedAt     string `json:"createdAt"`
	Target        string `json:"target"`
	// Redacted is true when IP and MAC addresses were replaced with stable
	// placeholders. Host, node, pod and object names are never redacted.
	Redacted bool   `json:"redacted"`
	Files    []File `json:"files"`
	// Notes state what the bundle does not contain.
	Notes []string `json:"notes"`
}

// File is one entry in the bundle.
type File struct {
	Name        string `json:"name"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	Description string `json:"description"`
}

// Builder collects files for a bundle.
type Builder struct {
	root     string
	manifest Manifest
	files    map[string][]byte
	redactor *Redactor
}

// New starts a bundle whose entries live under root/ in the archive. A
// non-nil redactor is applied to every file; share it with other output so
// placeholders agree.
func New(root, toolVersion, target string, created time.Time, redactor *Redactor) *Builder {
	b := &Builder{
		root:  root,
		files: map[string][]byte{},
		manifest: Manifest{
			SchemaVersion: SchemaVersion, ToolVersion: toolVersion, Target: target,
			CreatedAt: created.UTC().Format(time.RFC3339), Redacted: redactor != nil,
			Files: []File{}, Notes: []string{},
		},
		redactor: redactor,
	}
	return b
}

// Add adds a file. Content is redacted first when redaction is on.
func (b *Builder) Add(name, description string, content []byte) error {
	if _, dup := b.files[name]; dup {
		return fmt.Errorf("duplicate bundle entry %s", name)
	}
	if b.redactor != nil {
		content = b.redactor.Redact(content)
	}
	sum := sha256.Sum256(content)
	b.files[name] = content
	b.manifest.Files = append(b.manifest.Files, File{Name: name, Bytes: len(content), SHA256: hex.EncodeToString(sum[:]), Description: description})
	return nil
}

// AddJSON adds a value rendered as indented JSON.
func (b *Builder) AddJSON(name, description string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return b.Add(name, description, append(data, '\n'))
}

// Note records something the bundle does not contain.
func (b *Builder) Note(n string) { b.manifest.Notes = append(b.manifest.Notes, n) }

// Manifest returns the manifest as built so far.
func (b *Builder) Manifest() Manifest { return b.manifest }

// Write writes the archive. Entries are sorted and carry the creation time,
// so the same inputs produce the same archive.
func (b *Builder) Write(w io.Writer) error {
	if b.redactor != nil {
		b.Note(fmt.Sprintf("%d IP and %d MAC address(es) were replaced with placeholders consistently across all files", b.redactor.IPs(), b.redactor.MACs()))
	}
	sort.Slice(b.manifest.Files, func(i, j int) bool { return b.manifest.Files[i].Name < b.manifest.Files[j].Name })
	mf, err := json.MarshalIndent(b.manifest, "", "  ")
	if err != nil {
		return err
	}
	mtime, _ := time.Parse(time.RFC3339, b.manifest.CreatedAt)
	gz := gzip.NewWriter(w)
	gz.ModTime = mtime
	tw := tar.NewWriter(gz)
	put := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{
			Name: path.Join(b.root, name), Mode: 0o600, Size: int64(len(data)), ModTime: mtime, Typeflag: tar.TypeReg, Format: tar.FormatPAX,
		}); err != nil {
			return err
		}
		_, err := io.Copy(tw, bytes.NewReader(data))
		return err
	}
	if err := put("manifest.json", append(mf, '\n')); err != nil {
		return err
	}
	for _, f := range b.manifest.Files {
		if err := put(f.Name, b.files[f.Name]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}
