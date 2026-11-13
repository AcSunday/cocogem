package nativecli

import (
	"errors"
	"unicode/utf8"
)

// Bounded stream identity state. It keeps only byte length, a rolling digest
// and block type, never the visible body, so long replies stay O(1) per active
// block and the cache cannot grow with response length.
const maxTextStreamStates = 512

const (
	fnvOffset = uint64(14695981039346656037)
	fnvPrime  = uint64(1099511628211)
)

type textStreams struct {
	items map[string]*textStream
	order []string
	head  int
}

type textStream struct {
	length    uint64
	hash      uint64
	blockType string
}

func newTextStreams() *textStreams {
	return &textStreams{items: map[string]*textStream{}}
}

func (s *textStreams) state(key string) *textStream {
	if current, ok := s.items[key]; ok {
		return current
	}
	if len(s.items) >= maxTextStreamStates {
		delete(s.items, s.order[s.head])
		s.head++
		if s.head > maxTextStreamStates {
			s.order = append([]string(nil), s.order[s.head:]...)
			s.head = 0
		}
	}
	current := &textStream{}
	s.items[key] = current
	s.order = append(s.order, key)
	return current
}

func (s *textStreams) blockType(key string) string {
	if current, ok := s.items[key]; ok {
		return current.blockType
	}
	return ""
}

func (s *textStreams) setBlockType(key, blockType string) {
	if blockType == "" {
		return
	}
	s.state(key).blockType = blockType
}

func hashBytes(seed uint64, text string) uint64 {
	hash := seed
	for i := 0; i < len(text); i++ {
		hash = (hash ^ uint64(text[i])) * fnvPrime
	}
	return hash
}

func prefixHash(text string, length uint64) uint64 {
	if uint64(len(text)) < length {
		return 0
	}
	return hashBytes(fnvOffset, text[:length])
}

func advance(current *textStream, text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", errors.New("stream text is not valid UTF-8")
	}
	current.hash = hashBytes(current.hash, text)
	current.length += uint64(len(text))
	return text, nil
}

// delta records a stream fragment that the protocol defined as new bytes.
func (s *textStreams) delta(key, text string) (string, error) {
	if text == "" {
		return "", nil
	}
	current := s.state(key)
	if current.length == 0 {
		current.hash = fnvOffset
	}
	return advance(current, text)
}

// snapshotUnchecked extends a cumulative snapshot without re-hashing its full
// prefix. It is used for high-frequency intermediate updates; the final
// authoritative snapshot still verifies the prefix digest.
func (s *textStreams) snapshotUnchecked(key, text string) (string, error) {
	current := s.state(key)
	if uint64(len(text)) < current.length {
		return "", errors.New("native stream snapshot regressed")
	}
	if current.length == 0 {
		current.hash = fnvOffset
	}
	return advance(current, text[current.length:])
}

// covers reports how a complete text relates to what key already recorded:
// equal when it is exactly that text, extends when that text is its prefix. An
// unknown or still-empty key relates to nothing.
func (s *textStreams) covers(key, text string) (equal, extends bool) {
	current, ok := s.items[key]
	if !ok || current.length == 0 || uint64(len(text)) < current.length {
		return false, false
	}
	if prefixHash(text, current.length) != current.hash {
		return false, false
	}
	return uint64(len(text)) == current.length, true
}

// restart forgets what key recorded, so the next complete text opens a new
// block under the same key instead of being compared with the old one.
func (s *textStreams) restart(key string) {
	if current, ok := s.items[key]; ok {
		current.length, current.hash = 0, 0
	}
}

// authoritative reconciles a complete snapshot. A shorter body or a changed
// prefix is a protocol failure, not a silently discarded final answer.
func (s *textStreams) authoritative(key, text string) (suffix string, streamed bool, err error) {
	current, seen := s.items[key]
	if !seen || current.length == 0 {
		if !utf8.ValidString(text) {
			return "", false, errors.New("stream text is not valid UTF-8")
		}
		current = s.state(key)
		current.hash = hashBytes(fnvOffset, text)
		current.length = uint64(len(text))
		return text, false, nil
	}
	if uint64(len(text)) < current.length {
		return "", true, errors.New("native final snapshot is shorter than the streamed prefix")
	}
	if prefixHash(text, current.length) != current.hash {
		return "", true, errors.New("native final snapshot changed the streamed prefix")
	}
	suffix = text[current.length:]
	if !utf8.ValidString(suffix) {
		return "", true, errors.New("native final snapshot split a streamed UTF-8 character")
	}
	if suffix != "" {
		current.hash = hashBytes(current.hash, suffix)
		current.length += uint64(len(suffix))
	}
	return suffix, true, nil
}
