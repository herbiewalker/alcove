package trystbk

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Header is the 29-byte cleartext prefix of every TRYSTBK1 container.
//
// It carries the salt and the KDF cost needed to derive the key that authenticates the
// rest of the stream. Everything in it is ATTACKER-CONTROLLED when reading, so
// ParseHeader validates before returning.
type Header struct {
	Version    int
	Salt       [SaltSize]byte
	Iterations int
}

// Well-known parse errors. Callers pattern-match on these to tell "this was never a
// TRYSTBK1" (magic, version) from "the header claims something invalid" (iterations),
// which is also a hint that the file may be hostile.
var (
	ErrBadMagic        = errors.New("trystbk: bad magic, not a TRYSTBK1 container")
	ErrUnsupportedVer  = errors.New("trystbk: unsupported container version")
	ErrIterationsRange = errors.New("trystbk: PBKDF2 iterations out of bounds")
	ErrHeaderTruncated = errors.New("trystbk: header truncated")
)

// ParseHeader reads exactly HeaderSize bytes and returns a validated Header. After
// success r is positioned on the first byte of the AEAD stream.
func ParseHeader(r io.Reader) (Header, error) {
	var buf [HeaderSize]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return Header{}, ErrHeaderTruncated
		}
		return Header{}, fmt.Errorf("trystbk: read header: %w", err)
	}

	// Magic is the cheapest filter: a random file almost never starts with these 8
	// bytes, so it exits before any KDF work.
	if string(buf[0:MagicSize]) != Magic {
		return Header{}, ErrBadMagic
	}

	version := int(buf[MagicSize])
	if version != FormatVersion1 && version != FormatVersion2 {
		return Header{}, fmt.Errorf("%w: %d", ErrUnsupportedVer, version)
	}

	h := Header{Version: version}
	copy(h.Salt[:], buf[MagicSize+VersionSize:MagicSize+VersionSize+SaltSize])

	// Read as uint32 first: a value with the sign bit set is meaningless as a signed
	// count and would flip negative; catch it here as "out of bounds".
	iters := int64(binary.BigEndian.Uint32(buf[MagicSize+VersionSize+SaltSize:]))
	if iters < MinIterations || iters > MaxIterations {
		return Header{}, fmt.Errorf("%w: %d (accepted %d..%d)",
			ErrIterationsRange, iters, MinIterations, MaxIterations)
	}
	h.Iterations = int(iters)
	return h, nil
}

// Marshal returns the 29 header bytes for h. It refuses a header Tryst would refuse, so
// the writer can never produce a container the app rejects.
func (h Header) Marshal() ([]byte, error) {
	if h.Version != FormatVersion1 && h.Version != FormatVersion2 {
		return nil, fmt.Errorf("%w: %d", ErrUnsupportedVer, h.Version)
	}
	if h.Iterations < MinIterations || h.Iterations > MaxIterations {
		return nil, fmt.Errorf("%w: %d", ErrIterationsRange, h.Iterations)
	}
	out := make([]byte, 0, HeaderSize)
	out = append(out, Magic...)
	out = append(out, byte(h.Version))
	out = append(out, h.Salt[:]...)
	out = binary.BigEndian.AppendUint32(out, uint32(h.Iterations))
	return out, nil
}
