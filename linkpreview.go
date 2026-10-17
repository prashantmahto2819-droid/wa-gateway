package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	_ "golang.org/x/image/webp" // many sites serve og:image as WebP
	"golang.org/x/net/html"
	"google.golang.org/protobuf/proto"
)

// Link previews: when a text contains a link, WhatsApp phones show a card
// with the page's title, description and image. A linked device must build
// that card itself, so the gateway fetches the page (Open Graph tags), picks
// the image, shrinks it to a small JPEG and attaches it to the message.
//
// Like the phone (and Baileys), the card carries a tiny inline JPEG plus a
// larger "link thumbnail" uploaded to WhatsApp's media servers; without the
// upload, current WhatsApp versions often show the card with no image.
//
// Previews are cached for an hour (failures for 5 minutes) so a campaign to
// 1,000 people fetches the page once. Fetching uses the same private-network
// guard as media URLs.

type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image,omitempty"` // image URL that was used
	Thumb       []byte `json:"-"`               // small inline JPEG
	HQ          []byte `json:"-"`               // larger JPEG uploaded as the link thumbnail
	HQW, HQH    int    `json:"-"`
	FetchedAt   int64  `json:"fetched_at"`
	Error       string `json:"error,omitempty"`

	images  []string // candidate image URLs while fetching
	upMu    sync.Mutex
	uploads map[string]linkUpload // per linked number: the uploaded HQ thumbnail
}

type linkUpload struct {
	up whatsmeow.UploadResponse
	at time.Time
}

// previewLog is set by the manager so preview problems show in the server log.
var previewLog = slog.New(slog.NewTextHandler(io.Discard, nil))

var (
	urlRe        = regexp.MustCompile(`(?i)\bhttps?://[^\s<>"'()\[\]{}]+`)
	previewCache sync.Map // url -> *LinkPreview
	previewSem   = make(chan struct{}, 4)
)

// firstLink returns the first http(s) link in a text, or "".
func firstLink(text string) string {
	m := urlRe.FindString(text)
	return strings.TrimRight(m, ".,;:!?")
}

// linkPreview returns a cached or freshly fetched preview (nil if none can be built).
func linkPreview(ctx context.Context, raw string, allowPrivate bool) *LinkPreview {
	p := getPreview(ctx, raw, allowPrivate, false)
	if p == nil || p.Error != "" {
		return nil
	}
	return p
}

// getPreview is linkPreview that also returns failures (Error set), for diagnostics.
func getPreview(ctx context.Context, raw string, allowPrivate, fresh bool) *LinkPreview {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return &LinkPreview{URL: raw, Error: "not a valid link"}
	}
	key := u.String()
	if fresh {
		previewCache.Delete(key)
	}
	if v, ok := previewCache.Load(key); ok {
		p := v.(*LinkPreview)
		ttl := time.Hour
		if p.Error != "" {
			ttl = 5 * time.Minute // a slow site gets another chance soon
		}
		if time.Since(time.Unix(p.FetchedAt, 0)) < ttl {
			return p
		}
	}
	select {
	case previewSem <- struct{}{}:
		defer func() { <-previewSem }()
	case <-ctx.Done():
		return &LinkPreview{URL: key, Error: "busy, try again"}
	}
	fctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	defer cancel()
	// Sites differ in whom they show their tags to: try as WhatsApp, as a
	// normal browser (Cloudflare and similar often block "bots" on server
	// IPs), then as the Facebook crawler. Keep the first result with a picture.
	var p *LinkPreview
	var firstErr error
	for _, ua := range []string{uaWhatsApp, uaBrowser, uaFacebook} {
		got, err := fetchPreview(fctx, key, allowPrivate, ua)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			if fctx.Err() != nil {
				break
			}
			continue
		}
		if p == nil || (p.Title == "" && got.Title != "") || got.HQ != nil {
			p = got
		}
		if p.HQ != nil {
			break
		}
	}
	if p == nil {
		msg := "could not open the page"
		if firstErr != nil {
			msg = firstErr.Error()
		}
		p = &LinkPreview{URL: key, Error: msg}
	}
	p.FetchedAt = time.Now().Unix()
	previewCache.Store(key, p)
	return p
}

const (
	uaWhatsApp = "WhatsApp/2.2445.1 Mozilla/5.0 (compatible; VividRelay/1.0; +https://vividbrandify.com)"
	uaFacebook = "facebookexternalhit/1.1 (+http://www.facebook.com/externalhit_uatext.php)"
	uaBrowser  = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Mobile Safari/537.36"
)

func fetchPreview(ctx context.Context, link string, allowPrivate bool, ua string) (*LinkPreview, error) {
	client := previewClient(allowPrivate)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	// Many sites only give Open Graph tags to a WhatsApp / Facebook agent.
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	req.Header.Set("Accept-Language", "en-IN,hi;q=0.8,en;q=0.7")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, errors.New("page returned " + resp.Status)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "" && !strings.Contains(ct, "html") && !strings.Contains(ct, "xml") {
		if strings.HasPrefix(ct, "image/") { // a direct image link: preview is the image itself
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
			p := &LinkPreview{URL: link, Title: lastPathPart(link), Image: link}
			p.setImage(data)
			return p, nil
		}
		return nil, errors.New("not a web page")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	final := resp.Request.URL
	p := parseOG(body, final)
	p.URL = link
	if p.Title == "" {
		p.Title = final.Host
	}
	for _, img := range p.images {
		ireq, _ := http.NewRequestWithContext(ctx, http.MethodGet, img, nil)
		ireq.Header.Set("User-Agent", ua)
		ireq.Header.Set("Accept", "image/jpeg,image/png,image/webp,image/gif,image/*;q=0.8")
		ireq.Header.Set("Referer", final.String())
		if ir, err := client.Do(ireq); err == nil {
			data, _ := io.ReadAll(io.LimitReader(ir.Body, 8<<20))
			ir.Body.Close()
			if ir.StatusCode < 400 && p.setImage(data) {
				p.Image = img
				break
			}
		}
	}
	if p.HQ == nil {
		p.Image = ""
	}
	p.images = nil
	return p, nil
}

// setImage builds the inline and HQ thumbnails; false if data isn't a usable image.
func (p *LinkPreview) setImage(data []byte) bool {
	img := decodeImage(data)
	if img == nil {
		return false
	}
	b := img.Bounds()
	if b.Dx() < 16 || b.Dy() < 16 { // tracking pixels / spacers
		return false
	}
	p.Thumb = encodeJPEG(resize(img, 192, 192), 60_000)
	hq := resize(img, 1000, 1000)
	p.HQ = encodeJPEG(hq, 400_000)
	p.HQW, p.HQH = hq.Bounds().Dx(), hq.Bounds().Dy()
	return p.Thumb != nil && p.HQ != nil
}

func previewClient(allowPrivate bool) *http.Client {
	if allowPrivate {
		return &http.Client{Timeout: 12 * time.Second}
	}
	tr := &http.Transport{DialContext: guardedDialer().DialContext, TLSHandshakeTimeout: 8 * time.Second, Proxy: nil,
		ResponseHeaderTimeout: 10 * time.Second}
	return &http.Client{Timeout: 12 * time.Second, Transport: tr,
		CheckRedirect: func(_ *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			return nil
		}}
}

func lastPathPart(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return link
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if n := parts[len(parts)-1]; n != "" {
		return n
	}
	return u.Host
}

// parseOG reads Open Graph / Twitter / standard meta tags.
func parseOG(body []byte, base *url.URL) *LinkPreview {
	p := &LinkPreview{}
	doc, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return p
	}
	var title, ogTitle, ogDesc, metaDesc, ogImage, twImage, icon string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			attr := func(k string) string {
				for _, a := range n.Attr {
					if strings.EqualFold(a.Key, k) {
						return strings.TrimSpace(a.Val)
					}
				}
				return ""
			}
			switch n.Data {
			case "title":
				if n.FirstChild != nil && title == "" {
					title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "meta":
				prop, name, content := strings.ToLower(attr("property")), strings.ToLower(attr("name")), attr("content")
				switch {
				case prop == "og:title":
					ogTitle = content
				case prop == "og:description":
					ogDesc = content
				case name == "description":
					metaDesc = content
				case prop == "og:image" || prop == "og:image:url" || prop == "og:image:secure_url":
					if ogImage == "" {
						ogImage = content
					}
				case name == "twitter:image" || name == "twitter:image:src":
					twImage = content
				}
			case "link":
				rel := strings.ToLower(attr("rel"))
				if (strings.Contains(rel, "apple-touch-icon") || (strings.Contains(rel, "icon") && icon == "")) && attr("href") != "" {
					icon = attr("href")
				}
			case "body":
				return // meta tags live in <head>
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	p.Title = truncate(html.UnescapeString(firstNonEmpty(ogTitle, title)), 120)
	p.Description = truncate(html.UnescapeString(firstNonEmpty(ogDesc, metaDesc)), 300)
	seen := map[string]bool{}
	for _, img := range []string{ogImage, twImage, icon} {
		if img == "" {
			continue
		}
		if ref, err := url.Parse(strings.TrimSpace(img)); err == nil {
			abs := base.ResolveReference(ref)
			if (abs.Scheme == "http" || abs.Scheme == "https") && !seen[abs.String()] {
				seen[abs.String()] = true
				p.images = append(p.images, abs.String())
			}
		}
	}
	if len(p.images) > 0 {
		p.Image = p.images[0]
	}
	return p
}

// decodeImage decodes JPEG, PNG, GIF or WebP; nil if it isn't an image.
func decodeImage(data []byte) image.Image {
	if len(data) == 0 {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	if b := img.Bounds(); b.Dx() == 0 || b.Dy() == 0 {
		return nil
	}
	return img
}

// resize fits an image inside maxW x maxH on a white background (box sampling, no dependencies).
func resize(img image.Image, maxW, maxH int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	nw, nh := w, h
	if nw > maxW {
		nw, nh = maxW, max(1, h*maxW/w)
	}
	if nh > maxH {
		nw, nh = max(1, w*maxH/h), maxH
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src) // flatten transparency
	// For big sources, sample a fixed grid per output pixel so a 4000px photo stays fast.
	for y := 0; y < nh; y++ {
		sy0, sy1 := b.Min.Y+y*h/nh, b.Min.Y+(y+1)*h/nh
		stepY := max(1, (sy1-sy0)/4)
		for x := 0; x < nw; x++ {
			sx0, sx1 := b.Min.X+x*w/nw, b.Min.X+(x+1)*w/nw
			stepX := max(1, (sx1-sx0)/4)
			var r, g, bl, a, n uint64
			for sy := sy0; sy < max(sy1, sy0+1); sy += stepY {
				for sx := sx0; sx < max(sx1, sx0+1); sx += stepX {
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			if n == 0 {
				continue
			}
			al := float64(a/n) / 65535
			px := func(v uint64) uint8 { return uint8((float64(v/n)/65535*al + (1 - al)) * 255) }
			dst.Set(x, y, color.RGBA{px(r), px(g), px(bl), 255})
		}
	}
	return dst
}

// encodeJPEG encodes at the best quality that fits maxBytes.
func encodeJPEG(img image.Image, maxBytes int) []byte {
	var out bytes.Buffer
	for q := 85; q >= 35; q -= 10 {
		out.Reset()
		if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: q}); err != nil {
			return nil
		}
		if out.Len() <= maxBytes {
			break
		}
	}
	return out.Bytes()
}

// thumbnail shrinks an image to <= 300px JPEG; nil if not an image.
func thumbnail(data []byte) []byte {
	img := decodeImage(data)
	if img == nil {
		return nil
	}
	return encodeJPEG(resize(img, 300, 300), 60_000)
}

// mediaThumb is the small inline preview WhatsApp shows before a photo downloads.
func mediaThumb(data []byte) (thumb []byte, w, h int) {
	img := decodeImage(data)
	if img == nil {
		return nil, 0, 0
	}
	b := img.Bounds()
	return encodeJPEG(resize(img, 100, 100), 12_000), b.Dx(), b.Dy()
}

// applyLinkPreview turns a text message into one with a preview card when it contains a link.
func applyLinkPreview(ctx context.Context, cli *whatsmeow.Client, msg *waE2E.Message, text string, allowPrivate bool) {
	link := firstLink(text)
	if link == "" {
		return
	}
	p := getPreview(ctx, link, allowPrivate, false)
	if p == nil || p.Error != "" {
		if p != nil {
			previewLog.Warn("link preview not added", "url", link, "err", p.Error)
		}
		return
	}
	if p.HQ == nil {
		previewLog.Info("link preview has no image (the page gives none we can read)", "url", link, "image", p.Image)
	}
	ext := msg.ExtendedTextMessage
	if ext == nil {
		ext = &waE2E.ExtendedTextMessage{Text: proto.String(text)}
		msg.Conversation = nil
		msg.ExtendedTextMessage = ext
	}
	ext.MatchedText = proto.String(link)
	ext.Title = proto.String(p.Title)
	if p.Description != "" {
		ext.Description = proto.String(p.Description)
	}
	// Same shape the phone and Baileys send: preview type NONE, an inline
	// thumbnail, and the uploaded HQ thumbnail for the big card image.
	ext.PreviewType = waE2E.ExtendedTextMessage_NONE.Enum()
	if p.Thumb != nil {
		ext.JPEGThumbnail = p.Thumb
	}
	if up, ok := p.upload(ctx, cli); ok {
		ext.ThumbnailDirectPath = proto.String(up.DirectPath)
		ext.ThumbnailSHA256 = up.FileSHA256
		ext.ThumbnailEncSHA256 = up.FileEncSHA256
		ext.MediaKey = up.MediaKey
		ext.MediaKeyTimestamp = proto.Int64(time.Now().Unix())
		ext.ThumbnailWidth = proto.Uint32(uint32(p.HQW))
		ext.ThumbnailHeight = proto.Uint32(uint32(p.HQH))
	}
}

// upload sends the HQ thumbnail to WhatsApp once per linked number per hour.
func (p *LinkPreview) upload(ctx context.Context, cli *whatsmeow.Client) (whatsmeow.UploadResponse, bool) {
	if cli == nil || p.HQ == nil || cli.Store.ID == nil {
		return whatsmeow.UploadResponse{}, false
	}
	key := cli.Store.ID.User
	p.upMu.Lock()
	defer p.upMu.Unlock()
	if u, ok := p.uploads[key]; ok && time.Since(u.at) < time.Hour {
		return u.up, true
	}
	uctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	up, err := cli.Upload(uctx, p.HQ, whatsmeow.MediaLinkThumbnail)
	if err != nil {
		previewLog.Warn("link preview image upload failed (small image only)", "url", p.URL, "err", err)
		return whatsmeow.UploadResponse{}, false
	}
	if p.uploads == nil {
		p.uploads = map[string]linkUpload{}
	}
	p.uploads[key] = linkUpload{up: up, at: time.Now()}
	return up, true
}

// previewTest shows exactly what a link's preview card will contain, so the
// dashboard can display it before sending: GET /api/link-preview?url=&session=&fresh=1
func (a *API) previewTest(w http.ResponseWriter, r *http.Request) {
	link := strings.TrimSpace(r.URL.Query().Get("url"))
	if l := firstLink(link); l != "" {
		link = l
	}
	if link == "" {
		writeErr(w, http.StatusBadRequest, "bad_request", errors.New("url is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	p := getPreview(ctx, link, a.cfg.AllowPrivateURLs, r.URL.Query().Get("fresh") == "1")
	out := map[string]any{"ok": true, "url": link, "title": p.Title, "description": p.Description, "image": p.Image,
		"has_image": p.HQ != nil, "error": p.Error}
	if p.Thumb != nil {
		out["thumb"] = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(p.Thumb)
		out["hq_width"], out["hq_height"], out["hq_kb"] = p.HQW, p.HQH, len(p.HQ)/1024
	}
	switch {
	case p.Error != "":
		out["advice"] = "The server could not open this page (" + p.Error + "). The message will go without a preview card."
	case p.HQ == nil && p.Image != "":
		out["advice"] = "The page names an image but it could not be downloaded or read. Check that the og:image link opens in a browser."
	case p.HQ == nil:
		out["advice"] = "The page has no og:image tag, so the card will have no picture. Add <meta property=\"og:image\" content=\"https://…/photo.jpg\"> to the page, or send the photo with the link as its caption."
	}
	// With a connected number, also try the image upload WhatsApp needs for the big picture.
	if name := r.URL.Query().Get("session"); name != "" && p.HQ != nil {
		pr := principalOf(r)
		if s, err := a.m.get(name); err == nil && (s.owner() == pr.UserID || pr.Admin()) {
			if cli, err := s.ready(); err == nil {
				if _, ok := p.upload(ctx, cli); ok {
					out["upload"] = "ok"
				} else {
					out["upload"] = "failed"
				}
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}
