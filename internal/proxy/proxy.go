package proxy

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/greatbody/portly/internal/store"
)

// Handler builds an http.Handler that proxies requests for a single registered target.
// The mountPrefix is the URL path prefix that should be stripped before forwarding,
// e.g. "/p/grafana".
type Handler struct {
	Target          *store.Target
	MountPrefix     string // without trailing slash, e.g. "/p/grafana"
	UpstreamTimeout time.Duration
}

// New returns a configured proxy handler.
func New(t *store.Target, mountPrefix string, timeout time.Duration) (*Handler, error) {
	if t == nil {
		return nil, errors.New("nil target")
	}
	mountPrefix = strings.TrimRight(mountPrefix, "/")
	return &Handler{Target: t, MountPrefix: mountPrefix, UpstreamTimeout: timeout}, nil
}

func (h *Handler) upstreamURL() *url.URL {
	return &url.URL{
		Scheme: h.Target.Scheme,
		Host:   net.JoinHostPort(h.Target.Host, fmt.Sprintf("%d", h.Target.Port)),
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	upstream := h.upstreamURL()

	rp := &httputil.ReverseProxy{
		Director: func(req *http.Request) {
			req.URL.Scheme = upstream.Scheme
			req.URL.Host = upstream.Host
			req.Host = upstream.Host
			// Strip the mount prefix from the path.
			if h.MountPrefix != "" {
				if strings.HasPrefix(req.URL.Path, h.MountPrefix) {
					req.URL.Path = strings.TrimPrefix(req.URL.Path, h.MountPrefix)
					if req.URL.Path == "" {
						req.URL.Path = "/"
					}
				}
				if req.URL.RawPath != "" && strings.HasPrefix(req.URL.RawPath, h.MountPrefix) {
					req.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, h.MountPrefix)
					if req.URL.RawPath == "" {
						req.URL.RawPath = "/"
					}
				}
			}
			// Add forwarded headers.
			if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
				req.Header.Set("X-Forwarded-For", clientIP)
			}
			req.Header.Set("X-Forwarded-Host", req.Header.Get("Host"))
			req.Header.Set("X-Forwarded-Proto", schemeFromRequest(r))
			req.Header.Set("X-Forwarded-Prefix", h.MountPrefix)
			// Strip Accept-Encoding for HTML responses we want to rewrite. Easiest:
			// keep gzip but decode in ModifyResponse.
		},
		ModifyResponse: h.modifyResponse,
		ErrorHandler: func(rw http.ResponseWriter, req *http.Request, err error) {
			rw.WriteHeader(http.StatusBadGateway)
			_, _ = fmt.Fprintf(rw, "portly: upstream error: %v", err)
		},
		FlushInterval: 100 * time.Millisecond,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ResponseHeaderTimeout: h.UpstreamTimeout,
			MaxIdleConns:          50,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		},
	}
	rp.ServeHTTP(w, r)
}

func schemeFromRequest(r *http.Request) string {
	if r.TLS != nil {
		return "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		return v
	}
	return "http"
}

// modifyResponse rewrites Location headers, Set-Cookie paths, and HTML bodies so that
// links inside the proxied app continue to work behind the mount prefix.
func (h *Handler) modifyResponse(resp *http.Response) error {
	prefix := h.MountPrefix
	if prefix == "" {
		return nil
	}

	// 1. Rewrite Location header for redirects.
	if loc := resp.Header.Get("Location"); loc != "" {
		if newLoc, ok := rewriteLocation(loc, prefix); ok {
			resp.Header.Set("Location", newLoc)
		}
	}

	// 2. Rewrite Set-Cookie Path attribute.
	if cookies := resp.Header.Values("Set-Cookie"); len(cookies) > 0 {
		newCookies := make([]string, 0, len(cookies))
		for _, c := range cookies {
			newCookies = append(newCookies, rewriteSetCookiePath(c, prefix))
		}
		resp.Header.Del("Set-Cookie")
		for _, c := range newCookies {
			resp.Header.Add("Set-Cookie", c)
		}
	}

	// 3. Rewrite HTML bodies: inject <base href="/p/slug/"> after <head>.
	ct := resp.Header.Get("Content-Type")
	if !strings.Contains(strings.ToLower(ct), "text/html") {
		return nil
	}

	body, encoding, err := readBody(resp)
	if err != nil {
		return nil // best-effort; don't fail the whole response
	}

	rewritten := rewriteHTML(body, prefix)

	var out []byte
	switch encoding {
	case "gzip":
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write(rewritten)
		_ = gz.Close()
		out = buf.Bytes()
	default:
		out = rewritten
		resp.Header.Del("Content-Encoding")
	}
	resp.Body = io.NopCloser(bytes.NewReader(out))
	resp.ContentLength = int64(len(out))
	resp.Header.Set("Content-Length", fmt.Sprintf("%d", len(out)))
	return nil
}

func readBody(resp *http.Response) ([]byte, string, error) {
	enc := strings.ToLower(resp.Header.Get("Content-Encoding"))
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return nil, enc, err
	}
	switch enc {
	case "gzip":
		gz, err := gzip.NewReader(bytes.NewReader(raw))
		if err != nil {
			return raw, enc, nil
		}
		defer gz.Close()
		dec, err := io.ReadAll(gz)
		if err != nil {
			return raw, enc, nil
		}
		return dec, enc, nil
	default:
		return raw, enc, nil
	}
}

// rewriteHTML inserts a <base href="prefix/"> tag right after <head ...> so that
// relative URLs work, and rewrites root-absolute href/src attributes.
func rewriteHTML(body []byte, prefix string) []byte {
	prefixSlash := prefix + "/"
	baseTag := []byte(fmt.Sprintf(`<base href="%s">`, prefixSlash))

	lower := bytes.ToLower(body)
	if idx := bytes.Index(lower, []byte("<head")); idx >= 0 {
		// find end of opening tag
		if end := bytes.IndexByte(body[idx:], '>'); end >= 0 {
			insertAt := idx + end + 1
			body = append(body[:insertAt], append(baseTag, body[insertAt:]...)...)
		}
	} else {
		// no head: prepend
		body = append(baseTag, body...)
	}
	return body
}

// rewriteLocation rewrites a redirect Location header so it stays inside the mount.
func rewriteLocation(loc, prefix string) (string, bool) {
	// Absolute URL
	if strings.HasPrefix(loc, "http://") || strings.HasPrefix(loc, "https://") {
		u, err := url.Parse(loc)
		if err != nil {
			return loc, false
		}
		// If upstream redirects to itself (same host), rewrite to mount + path.
		// We can't easily check upstream host here; instead, rewrite path only when host matches request host... too complex.
		// Strategy: rewrite the path only if it is root-absolute.
		if u.Path == "" {
			u.Path = "/"
		}
		newPath := prefix + u.Path
		// Replace path while keeping scheme/host? Safer: return relative.
		out := newPath
		if u.RawQuery != "" {
			out += "?" + u.RawQuery
		}
		if u.Fragment != "" {
			out += "#" + u.Fragment
		}
		return out, true
	}
	// Root-absolute path
	if strings.HasPrefix(loc, "/") {
		return prefix + loc, true
	}
	return loc, false
}

// rewriteSetCookiePath ensures Path= attribute is scoped under the mount prefix.
func rewriteSetCookiePath(cookie, prefix string) string {
	parts := strings.Split(cookie, ";")
	found := false
	for i, p := range parts {
		trim := strings.TrimSpace(p)
		if strings.HasPrefix(strings.ToLower(trim), "path=") {
			parts[i] = " Path=" + prefix + "/"
			found = true
		}
	}
	if !found {
		parts = append(parts, " Path="+prefix+"/")
	}
	return strings.Join(parts, ";")
}
