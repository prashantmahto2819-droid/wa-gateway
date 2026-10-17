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
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow/proto/waE2E"
	"golang.org/x/net/html"
	"google.golang.org/protobuf/proto"
)

// Link previews: when a text contains a link, WhatsApp phones show a card
// with the page's title, description and image. A linked device must build
// that card itself, so the gateway fetches the page (Open Graph tags), picks
// the image, shrinks it to a small JPEG and attaches it to the message.
//
// Previews are cached for an hour so a campaign to 1,000 people fetches the
// page once. Fetching uses the same private-network guard as media URLs.

type LinkPreview struct {
	URL         string `json:"url"`
	Title       string `json:"title"`
	Description string `json:"description"`
	Image       string `json:"image,omitempty"` // image URL that was used
	Thumb       []byte `json:"-"`               // small JPEG (<= 60 KB)
	FetchedAt   int64  `json:"fetched_at"`
	Error       string `json:"error,omitempty"`
}

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
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil
	}
	key := u.String()
	if v, ok := previewCache.Load(key); ok {
		p := v.(*LinkPreview)
		if time.Since(time.Unix(p.FetchedAt, 0)) < time.Hour {
			if p.Error != "" {
				return nil
			}
			return p
		}
	}
	select {
	case previewSem <- struct{}{}:
		defer func() { <-previewSem }()
	case <-ctx.Done():
		return nil
	}
	fctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	p, err := fetchPreview(fctx, key, allowPrivate)
	if err != nil {
		p = &LinkPreview{URL: key, Error: err.Error()}
	}
	p.FetchedAt = time.Now().Unix()
	previewCache.Store(key, p)
	if p.Error != "" {
		return nil
	}
	return p
}

func fetchPreview(ctx context.Context, link string, allowPrivate bool) (*LinkPreview, error) {
	client := previewClient(allowPrivate)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, link, nil)
	// Many sites only give Open Graph tags to a "browser-like" or WhatsApp agent.
	req.Header.Set("User-Agent", "WhatsApp/2.24.0 Mozilla/5.0 (compatible; VividRelay/1.0; +https://vividbrandify.com)")
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
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 6<<20))
			p := &LinkPreview{URL: link, Title: lastPathPart(link), Image: link}
			p.Thumb = thumbnail(data)
			return p, nil
		}
		return nil, errors.New("not a web page")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	final := resp.Request.URL
	p := parseOG(body, final)
	p.URL = link
	if p.Title == "" {
		p.Title = final.Host
	}
	if p.Image != "" {
		ireq, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.Image, nil)
		ireq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; VividRelay/1.0)")
		if ir, err := client.Do(ireq); err == nil {
			data, _ := io.ReadAll(io.LimitReader(ir.Body, 6<<20))
			ir.Body.Close()
			if ir.StatusCode < 400 {
				p.Thumb = thumbnail(data)
			}
		}
		if p.Thumb == nil {
			p.Image = ""
		}
	}
	return p, nil
}

func previewClient(allowPrivate bool) *http.Client {
	if allowPrivate {
		return &http.Client{Timeout: 12 * time.Second}
	}
	tr := &http.Transport{DialContext: guardedDialer().DialContext, TLSHandshakeTimeout: 8 * time.Second, Proxy: nil}
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
	p.Title = truncate(firstNonEmpty(ogTitle, title), 120)
	p.Description = truncate(firstNonEmpty(ogDesc, metaDesc), 300)
	for _, img := range []string{ogImage, twImage, icon} {
		if img == "" {
			continue
		}
		if ref, err := url.Parse(img); err == nil {
			abs := base.ResolveReference(ref)
			if abs.Scheme == "http" || abs.Scheme == "https" {
				p.Image = abs.String()
				break
			}
		}
	}
	return p
}

// thumbnail shrinks an image to <= 300px wide JPEG (what WhatsApp expects); nil if not an image.
func thumbnail(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == 0 || h == 0 {
		return nil
	}
	const maxW = 300
	nw, nh := w, h
	if w > maxW {
		nw = maxW
		nh = max(1, h*maxW/w)
	}
	if nh > 300 {
		nh = 300
		nw = max(1, w*300/h)
	}
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src) // flatten transparency
	// Simple box sampling: good enough for a 300px thumbnail and has no dependencies.
	for y := 0; y < nh; y++ {
		sy0, sy1 := b.Min.Y+y*h/nh, b.Min.Y+(y+1)*h/nh
		for x := 0; x < nw; x++ {
			sx0, sx1 := b.Min.X+x*w/nw, b.Min.X+(x+1)*w/nw
			var r, g, bl, a, n uint64
			for sy := sy0; sy < max(sy1, sy0+1); sy++ {
				for sx := sx0; sx < max(sx1, sx0+1); sx++ {
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
	var out bytes.Buffer
	for q := 80; q >= 40; q -= 20 {
		out.Reset()
		if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: q}); err != nil {
			return nil
		}
		if out.Len() <= 60_000 {
			break
		}
	}
	return out.Bytes()
}

// applyLinkPreview turns a text message into one with a preview card when it contains a link.
func applyLinkPreview(ctx context.Context, msg *waE2E.Message, text string, allowPrivate bool) {
	link := firstLink(text)
	if link == "" {
		return
	}
	p := linkPreview(ctx, link, allowPrivate)
	if p == nil {
		return
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
	if p.Thumb != nil {
		ext.JPEGThumbnail = p.Thumb
		ext.PreviewType = waE2E.ExtendedTextMessage_IMAGE.Enum()
	} else {
		ext.PreviewType = waE2E.ExtendedTextMessage_NONE.Enum()
	}
}
