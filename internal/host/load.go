package host

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
)

// LoadSnapshot reads a snapshot file written by "kubectl netdoctor host
// collect" and validates its schema version and node name.
func LoadSnapshot(path string) (*Snapshot, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- path is an explicit CLI argument
	if err != nil {
		return nil, fmt.Errorf("reading host snapshot: %w", err)
	}
	return DecodeSnapshot(data)
}

// DecodeSnapshot decodes and validates snapshot JSON.
func DecodeSnapshot(data []byte) (*Snapshot, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var s Snapshot
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("parsing host snapshot: %w", err)
	}
	if s.SchemaVersion != SchemaVersion {
		return nil, fmt.Errorf("host snapshot has schemaVersion %q, expected %q", s.SchemaVersion, SchemaVersion)
	}
	if s.NodeName == "" {
		return nil, fmt.Errorf("host snapshot has no nodeName; re-collect with --node-name")
	}
	return &s, nil
}
