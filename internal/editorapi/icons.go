package editorapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"2t5-deck-plus/internal/preset"
)

// ErrNoIcon is what an IconProvider returns for an app it cannot find.
var ErrNoIcon = errors.New("no icon for this app")

// IconProvider renders an app icon as a PNG (about 256 px square).
// The app implements it with NSWorkspace (macapp.AppIconPNG).
type IconProvider interface {
	AppIconPNG(bundleID string) ([]byte, error)
}

const (
	iconTTL     = 30 * 24 * time.Hour
	missTTL     = time.Minute
	maxIconSize = 2 << 20
	iconMaster  = 256
)

type iconCache struct {
	mu     sync.Mutex
	misses map[string]time.Time
}

var pngMagic = []byte("\x89PNG\r\n\x1a\n")

func okBundleID(id string) bool { return id != "" && len(id) <= 200 && preset.ValidID(id) }

func iconPath(dir, id string, size int) string {
	if size == iconMaster {
		return filepath.Join(dir, ".icons", id+".png")
	}
	return filepath.Join(dir, ".icons", id+"@"+strconv.Itoa(size)+".png")
}

func fresh(path string) ([]byte, bool) {
	fi, err := os.Stat(path)
	if err != nil || time.Since(fi.ModTime()) > iconTTL {
		return nil, false
	}
	b, err := os.ReadFile(path)
	if err != nil || !bytes.HasPrefix(b, pngMagic) {
		return nil, false
	}
	return b, true
}

// icon returns the PNG for id at size (256 = the master). The installed app's own icon (the provider,
// cached on disk under <presets dir>/.icons/) always wins; an app that is not on this Mac falls back
// to the icon bundled with its standard preset (never written to the disk cache, so it never reads
// as installed).
func (s *server) icon(id string, size int) ([]byte, error) {
	if master, ok := s.providerMaster(id); ok {
		return s.sized(id, master, size, true)
	}
	if b := preset.TemplateIconPNG(id); b != nil {
		return s.sized(id, b, size, false)
	}
	return nil, ErrNoIcon
}

// installed reports whether the app with this bundle id is on this Mac (its own icon can be read).
func (s *server) installed(id string) bool {
	if !okBundleID(id) {
		return false
	}
	_, ok := s.providerMaster(id)
	return ok
}

// providerMaster is the 256 px master of the installed app's icon, from the disk cache or the provider.
func (s *server) providerMaster(id string) ([]byte, bool) {
	if s.d.Icons == nil {
		return nil, false
	}
	dir := s.d.Store.Dir()
	if b, ok := fresh(iconPath(dir, id, iconMaster)); ok {
		return b, true
	}
	s.ic.mu.Lock()
	defer s.ic.mu.Unlock()
	if b, ok := fresh(iconPath(dir, id, iconMaster)); ok {
		return b, true
	}
	if t, ok := s.ic.misses[id]; ok && time.Since(t) < missTTL {
		return nil, false
	}
	b, err := s.d.Icons.AppIconPNG(id)
	if err != nil || len(b) == 0 || len(b) > maxIconSize || !bytes.HasPrefix(b, pngMagic) {
		if s.ic.misses == nil {
			s.ic.misses = map[string]time.Time{}
		}
		s.ic.misses[id] = time.Now()
		return nil, false
	}
	s.cacheWrite(dir, iconPath(dir, id, iconMaster), b)
	return b, true
}

// sized returns master at size, caching the shrunk copy on disk when cache is true.
func (s *server) sized(id string, master []byte, size int, cache bool) ([]byte, error) {
	if size == iconMaster {
		return master, nil
	}
	if cache {
		if b, ok := fresh(iconPath(s.d.Store.Dir(), id, size)); ok {
			return b, nil
		}
	}
	out, err := resizePNG(master, size)
	if err != nil {
		return master, nil // a master we cannot shrink is still an icon
	}
	if cache {
		s.cacheWrite(s.d.Store.Dir(), iconPath(s.d.Store.Dir(), id, size), out)
	}
	return out, nil
}

func (s *server) cacheWrite(dir, path string, b []byte) {
	d := filepath.Join(dir, ".icons")
	if err := os.Mkdir(d, 0o755); err != nil && !os.IsExist(err) { // parent (presets dir) exists
		return
	}
	_ = preset.WriteAtomic(path, b)
}

// resizePNG shrinks a square-ish PNG to size x size with an area average.
func resizePNG(src []byte, size int) ([]byte, error) {
	img, err := png.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	sb := img.Bounds()
	in := image.NewNRGBA(image.Rect(0, 0, sb.Dx(), sb.Dy()))
	draw.Draw(in, in.Bounds(), img, sb.Min, draw.Src)
	out := image.NewNRGBA(image.Rect(0, 0, size, size))
	sw, sh := float64(sb.Dx())/float64(size), float64(sb.Dy())/float64(size)
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			x0, x1 := int(float64(x)*sw), int(float64(x+1)*sw)
			y0, y1 := int(float64(y)*sh), int(float64(y+1)*sh)
			if x1 <= x0 {
				x1 = x0 + 1
			}
			if y1 <= y0 {
				y1 = y0 + 1
			}
			var r, g, b, a, n float64
			for yy := y0; yy < y1 && yy < sb.Dy(); yy++ {
				for xx := x0; xx < x1 && xx < sb.Dx(); xx++ {
					p := in.NRGBAAt(xx, yy)
					al := float64(p.A)
					r += float64(p.R) * al
					g += float64(p.G) * al
					b += float64(p.B) * al
					a += al
					n++
				}
			}
			if n == 0 || a == 0 {
				continue
			}
			out.SetNRGBA(x, y, nrgba(r/a, g/a, b/a, a/n))
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, out); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func serveIcon(w http.ResponseWriter, r *http.Request, b []byte) {
	sum := sha256.Sum256(b)
	etag := `"` + hex.EncodeToString(sum[:8]) + `"`
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=3600")
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		for _, c := range strings.Split(inm, ",") {
			if strings.TrimPrefix(strings.TrimSpace(c), "W/") == etag {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	_, _ = w.Write(b)
}

func (s *server) serveIconFor(w http.ResponseWriter, r *http.Request, raw string, size int) {
	id := strings.TrimSuffix(raw, ".png")
	if !okBundleID(id) {
		writeErr(w, http.StatusBadRequest, "id", "not a valid bundle id")
		return
	}
	b, err := s.icon(id, size)
	if err != nil {
		w.Header().Set("Cache-Control", "no-store")
		writeErr(w, http.StatusNotFound, "id", "no icon for "+id)
		return
	}
	serveIcon(w, r, b)
}

// GET /api/icon/{bundleId}.png : the 256 px master.
func (s *server) iconMaster(w http.ResponseWriter, r *http.Request) {
	s.serveIconFor(w, r, r.PathValue("file"), iconMaster)
}

// GET /api/appicon/{bundleId}?size=64 : 32..256 px.
func (s *server) appIcon(w http.ResponseWriter, r *http.Request) {
	size := 64
	if q := r.URL.Query().Get("size"); q != "" {
		n, err := strconv.Atoi(q)
		if err != nil || n < 32 || n > 256 {
			writeErr(w, http.StatusBadRequest, "size", "size must be a whole number from 32 to 256")
			return
		}
		size = n
	}
	s.serveIconFor(w, r, r.PathValue("file"), size)
}

func nrgba(r, g, b, a float64) (c color.NRGBA) {
	return color.NRGBA{R: uint8(r + .5), G: uint8(g + .5), B: uint8(b + .5), A: uint8(a + .5)}
}
