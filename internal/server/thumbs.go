package server

import (
	"container/list"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/herbiewalker/alcove/internal/photo"
)

// Thumbnails (D-44) are made when first asked for, from the stored picture, and kept only in memory
// for a short while. Nothing about them is written to disk, so a purge has nothing extra to remove,
// and a restart simply makes them again.
const (
	thumbEdge     = 360              // longest side in pixels
	thumbKeep     = 10 * time.Minute // how long a made thumbnail is reused
	thumbMaxBytes = 32 << 20         // the cache never holds more than this
	thumbMaxCount = 512
	thumbWorkers  = 2 // pictures decoded at once; each can need up to ~200 MB
)

type thumbEntry struct {
	key  string
	data []byte
	at   time.Time
}

// thumbCache is a small least-recently-used cache with an age limit.
type thumbCache struct {
	mu    sync.Mutex
	now   func() time.Time
	order *list.List // front is the newest
	byKey map[string]*list.Element
	bytes int
}

func newThumbCache(now func() time.Time) *thumbCache {
	return &thumbCache{now: now, order: list.New(), byKey: map[string]*list.Element{}}
}

func (c *thumbCache) get(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.byKey[key]
	if !ok {
		return nil, false
	}
	e := el.Value.(*thumbEntry)
	if c.now().Sub(e.at) > thumbKeep {
		c.remove(el)
		return nil, false
	}
	c.order.MoveToFront(el)
	return e.data, true
}

func (c *thumbCache) put(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if el, ok := c.byKey[key]; ok {
		c.remove(el)
	}
	c.byKey[key] = c.order.PushFront(&thumbEntry{key: key, data: data, at: c.now()})
	c.bytes += len(data)
	for c.order.Len() > thumbMaxCount || c.bytes > thumbMaxBytes {
		c.remove(c.order.Back())
	}
}

func (c *thumbCache) remove(el *list.Element) {
	e := el.Value.(*thumbEntry)
	c.bytes -= len(e.data)
	delete(c.byKey, e.key)
	c.order.Remove(el)
}

// sendThumb writes the thumbnail of one stored photo and reports whether it did. It is called only
// after the same gate as the full picture has passed. When it cannot make one (not decodable, too
// large) it writes nothing and returns false, and the caller sends the picture itself.
func (s *Server) sendThumb(q *rq, owner, id string) bool {
	key := owner + "/" + id
	data, ok := s.thumbs.get(key)
	if !ok {
		select {
		case s.thumbSem <- struct{}{}:
			defer func() { <-s.thumbSem }()
		case <-q.r.Context().Done():
			return false
		}
		if data, ok = s.thumbs.get(key); !ok { // another request may have made it while this one waited
			rc, opened := s.openPhotoQuiet(q, owner, id)
			if !opened {
				return false
			}
			raw, err := io.ReadAll(io.LimitReader(rc, maxServedPhoto+1))
			_ = rc.Close()
			if err != nil || len(raw) > maxServedPhoto {
				return false
			}
			if data, err = photo.Thumbnail(raw, thumbEdge); err != nil {
				s.log.Warn("a thumbnail could not be made; sending the picture", "owner", owner)
				return false
			}
			s.thumbs.put(key, data)
		}
	}
	h := q.w.Header()
	setPhotoHeaders(h)
	h.Set("Content-Type", "image/jpeg")
	h.Set("Content-Length", strconv.Itoa(len(data)))
	_, _ = q.w.Write(data)
	return true
}
