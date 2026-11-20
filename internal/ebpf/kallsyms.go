package ebpf

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
)

// kallsyms resolves kernel text addresses to function+offset.
type kallsyms struct {
	addrs []uint64
	names []string
}

func loadKallsyms(path string) (*kallsyms, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return parseKallsyms(f)
}

func parseKallsyms(r io.Reader) (*kallsyms, error) {
	type sym struct {
		addr uint64
		name string
	}
	var syms []sym
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < 3 {
			continue
		}
		if t := fields[1]; t != "t" && t != "T" {
			continue
		}
		a, err := strconv.ParseUint(fields[0], 16, 64)
		if err != nil || a == 0 {
			continue // addresses hidden by kptr_restrict read as zero
		}
		syms = append(syms, sym{a, fields[2]})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(syms) == 0 {
		return nil, fmt.Errorf("no kernel symbol addresses readable (kptr_restrict)")
	}
	sort.Slice(syms, func(i, j int) bool { return syms[i].addr < syms[j].addr })
	k := &kallsyms{addrs: make([]uint64, len(syms)), names: make([]string, len(syms))}
	for i, s := range syms {
		k.addrs[i], k.names[i] = s.addr, s.name
	}
	return k, nil
}

func (k *kallsyms) resolve(addr uint64) string {
	i := sort.Search(len(k.addrs), func(i int) bool { return k.addrs[i] > addr }) - 1
	if i < 0 {
		return fmt.Sprintf("0x%x", addr)
	}
	return fmt.Sprintf("%s+0x%x", k.names[i], addr-k.addrs[i])
}

// decodeLE decodes a little-endian ring buffer sample into a fixed-size
// struct. Both supported architectures (amd64, arm64) are little-endian.
func decodeLE(b []byte, v any) error {
	if len(b) < binary.Size(v) {
		return fmt.Errorf("short sample: %d bytes", len(b))
	}
	return binary.Read(bytes.NewReader(b), binary.LittleEndian, v)
}
