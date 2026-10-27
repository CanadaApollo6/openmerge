package journal

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"encoding/hex"
	"io"
)

// IDs are arbitrary strings, not monotonic counters. A watermark, hash or
// probabilistic filter cannot replace exact rejection fences. This lossless
// table front-codes sorted keys and compresses the result. Anchors survive too.
// It is a derived checkpoint format only; immutable events never change.
const maxFenceTableBytes = 128 << 20
const maxFenceEntries = 2 << 20

func encodeFenceTable(c checkpoint) []byte {
	buf := []byte{1}
	for _, fences := range [][]fence{c.Generations, c.Settled} {
		buf = binary.AppendUvarint(buf, uint64(len(fences)))
		previous := ""
		for _, f := range fences {
			prefix := 0
			for prefix < len(previous) && prefix < len(f.Key) && previous[prefix] == f.Key[prefix] {
				prefix++
			}
			buf = binary.AppendUvarint(buf, uint64(prefix))
			suffix := packFenceKey(f.Key[prefix:])
			buf = binary.AppendUvarint(buf, uint64(len(suffix)))
			buf = append(buf, suffix...)
			buf = binary.AppendUvarint(buf, f.ID)
			previous = f.Key
		}
	}
	return buf
}

func packFences(c checkpoint) []byte {
	var out bytes.Buffer
	w := zlib.NewWriter(&out)
	_, _ = w.Write(encodeFenceTable(c))
	_ = w.Close()
	return out.Bytes()
}

func unpackFences(raw []byte, through uint64) (checkpoint, error) {
	c := checkpoint{Through: through}
	compressed := bytes.NewReader(raw)
	r, err := zlib.NewReader(compressed)
	if err != nil {
		return c, ErrMalformed
	}
	buf, err := io.ReadAll(io.LimitReader(r, maxFenceTableBytes+1))
	closeErr := r.Close()
	if err != nil || closeErr != nil || len(buf) > maxFenceTableBytes || len(buf) == 0 || buf[0] != 1 {
		return c, ErrLimit
	}
	payload := buf
	buf = buf[1:]
	read := func() (uint64, bool) {
		n, size := binary.Uvarint(buf)
		if size <= 0 {
			return 0, false
		}
		buf = buf[size:]
		return n, true
	}
	expanded, entries := 0, uint64(0)
	for _, target := range []*[]fence{&c.Generations, &c.Settled} {
		count, ok := read()
		if count > maxFenceEntries-entries {
			return c, ErrLimit
		}
		entries += count
		// Every row needs at least three varints; reject malicious allocation
		// counts before allocating, including empty/truncated tables.
		if !ok || count > uint64(len(buf)/3) {
			return c, ErrMalformed
		}
		*target = make([]fence, 0, int(count))
		previous := ""
		for range count {
			prefix, ok := read()
			if !ok || prefix > uint64(len(previous)) {
				return c, ErrMalformed
			}
			size, ok := read()
			if !ok || size > uint64(len(buf)) {
				return c, ErrMalformed
			}
			suffix, err := unpackFenceKey(buf[:int(size)])
			if err != nil || int(prefix)+len(suffix) > maxFenceTableBytes {
				return c, ErrMalformed
			}
			key := previous[:int(prefix)] + suffix
			expanded += len(key)
			if expanded > maxFenceTableBytes {
				return c, ErrLimit
			}
			buf = buf[int(size):]
			id, ok := read()
			if !ok || key <= previous {
				return c, ErrMalformed
			}
			*target = append(*target, fence{key, id})
			previous = key
		}
	}
	// Canonicalize the lossless table, not the compressor's output: valid
	// streams written by other Go/zlib versions must remain readable.
	if len(buf) != 0 || compressed.Len() != 0 || !bytes.Equal(payload, encodeFenceTable(c)) {
		return c, ErrMalformed
	}
	return c, nil
}

// Long lowercase hex runs are common in change/generation/attempt identities.
// Encode them as nibbles before compression. All other bytes (including Unicode,
// uppercase hex, punctuation and JSON escapes) survive literally. No hash or
// interpretation of an identifier is involved.
func packFenceKey(key string) []byte {
	var out []byte
	rawStart := 0
	for i := 0; i < len(key); {
		start := i
		for i < len(key) && (key[i] >= '0' && key[i] <= '9' || key[i] >= 'a' && key[i] <= 'f') {
			i++
		}
		if i-start < 16 {
			if i == start {
				i++
			}
			continue
		}
		if start > rawStart {
			out = binary.AppendUvarint(out, uint64(start-rawStart)<<1)
			out = append(out, key[rawStart:start]...)
		}
		out = binary.AppendUvarint(out, uint64(i-start)<<1|1)
		run := key[start:i]
		if len(run)%2 != 0 {
			run += "0"
		}
		packed, _ := hex.DecodeString(run)
		out = append(out, packed...)
		rawStart = i
	}
	if rawStart < len(key) {
		out = binary.AppendUvarint(out, uint64(len(key)-rawStart)<<1)
		out = append(out, key[rawStart:]...)
	}
	return out
}

func unpackFenceKey(raw []byte) (string, error) {
	var out []byte
	for len(raw) > 0 {
		header, size := binary.Uvarint(raw)
		if size <= 0 {
			return "", ErrMalformed
		}
		raw = raw[size:]
		count := header >> 1
		if count == 0 || count > maxFenceTableBytes-uint64(len(out)) {
			return "", ErrMalformed
		}
		n := count
		if header&1 != 0 {
			n = (n + 1) / 2
		}
		if n > uint64(len(raw)) {
			return "", ErrMalformed
		}
		if header&1 != 0 {
			out = append(out, hex.EncodeToString(raw[:int(n)])[:int(count)]...)
		} else {
			out = append(out, raw[:int(n)]...)
		}
		raw = raw[int(n):]
	}
	return string(out), nil
}
