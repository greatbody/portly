package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"regexp"
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
		FlushInterval: -1, // immediate flush; required for SSE / streaming, harmless otherwise
		Transport:     http.DefaultTransport,
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
	ct := strings.ToLower(resp.Header.Get("Content-Type"))

	// Streaming responses (SSE, etc): touch headers only, never read body.
	if strings.Contains(ct, "text/event-stream") {
		return nil
	}

	// 1. Rewrite Location header for redirects.
	if loc := resp.Header.Get("Location"); loc != "" {
		if newLoc, ok := rewriteLocation(loc, prefix); ok {
			resp.Header.Set("Location", newLoc)
		}
	}

	// 1b. Strip CSP / frame-busting headers so our injected shim can run and
	// so the app stays usable behind the mount prefix. portly is itself the
	// trust boundary; the upstream's CSP no longer matches the served origin.
	resp.Header.Del("Content-Security-Policy")
	resp.Header.Del("Content-Security-Policy-Report-Only")
	resp.Header.Del("X-Frame-Options")
	resp.Header.Del("Cross-Origin-Opener-Policy")
	resp.Header.Del("Cross-Origin-Embedder-Policy")
	resp.Header.Del("Cross-Origin-Resource-Policy")

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
	if !strings.Contains(ct, "text/html") {
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

// rewriteHTML rewrites root-absolute attribute URLs to include the mount prefix and
// injects a runtime shim that patches fetch / XHR / WebSocket / EventSource so that
// SPA JavaScript that builds absolute URLs at runtime also stays inside the mount.
func rewriteHTML(body []byte, prefix string) []byte {
	prefixSlash := prefix + "/"

	// 1. Rewrite root-absolute attributes: src="/x", href="/x", action="/x", srcset, content
	//    Skip protocol-relative ("//x") and double-slashes inside data: URIs.
	attrRe := regexp.MustCompile(`(\s(?:src|href|action|poster|formaction|data-src|data-href)\s*=\s*)("|')/([^"'/])`)
	body = attrRe.ReplaceAll(body, []byte(`${1}${2}`+prefixSlash+`${3}`))

	// srcset: comma-separated "/path size, /path2 size2"
	srcsetRe := regexp.MustCompile(`(\ssrcset\s*=\s*"|\ssrcset\s*=\s*')([^"']+)(['"])`)
	body = srcsetRe.ReplaceAllFunc(body, func(m []byte) []byte {
		sub := srcsetRe.FindSubmatch(m)
		parts := strings.Split(string(sub[2]), ",")
		for i, p := range parts {
			tp := strings.TrimSpace(p)
			if strings.HasPrefix(tp, "/") && !strings.HasPrefix(tp, "//") {
				parts[i] = " " + prefixSlash + strings.TrimPrefix(tp, "/")
			}
		}
		return []byte(string(sub[1]) + strings.Join(parts, ",") + string(sub[3]))
	})

	// 2. Inject shim + <base> right after <head ...> (or prepend if no head).
	shim := buildShim(prefixSlash)
	baseTag := []byte(fmt.Sprintf(`<base href="%s">`, prefixSlash))
	insert := append(baseTag, shim...)

	lower := bytes.ToLower(body)
	if idx := bytes.Index(lower, []byte("<head")); idx >= 0 {
		if end := bytes.IndexByte(body[idx:], '>'); end >= 0 {
			insertAt := idx + end + 1
			body = append(body[:insertAt], append(insert, body[insertAt:]...)...)
		}
	} else {
		body = append(insert, body...)
	}
	return body
}

// buildShim returns a tiny inline <script> that monkey-patches the browser APIs
// most likely to leak out of the mount prefix at runtime: fetch, XHR.open,
// WebSocket, EventSource, and history.pushState/replaceState.
func buildShim(prefixSlash string) []byte {
	// prefixSlash already ends with "/"
	js := `<script>(function(){
var P=` + jsString(prefixSlash) + `;
var ORIGIN=location.origin;
function toLocal(u){
  // Absolute URL pointing at some other host: capture path+query+hash and
  // re-anchor to current origin under the mount prefix. This is essential
  // for SPAs that hardcode an upstream baseUrl (e.g. http://localhost:4096
  // or http://10.x.x.x:port) into JS or stash one in localStorage.
  try{
    var url=new URL(u);
    var path=url.pathname||"/";
    if(path.indexOf(P)!==0)path=P+path.replace(/^\//,"");
    return ORIGIN+path+(url.search||"")+(url.hash||"");
  }catch(e){return u;}
}
function fix(u){
  if(typeof u!=="string")return u;
  if(u.indexOf("data:")===0||u.indexOf("blob:")===0||u.indexOf("javascript:")===0)return u;
  // Absolute http(s) URL
  if(/^https?:\/\//i.test(u)){
    if(u.indexOf(ORIGIN+P)===0)return u; // already correct
    return toLocal(u);
  }
  // Protocol-relative //host/x
  if(u.indexOf("//")===0)return toLocal(location.protocol+u);
  // Already prefixed
  if(u.indexOf(P)===0)return u;
  // Root-absolute /x
  if(u.length>0&&u.charAt(0)==="/")return P+u.substring(1);
  return u;
}
function fixWS(u){
  if(typeof u!=="string")return u;
  var wsScheme=location.protocol==="https:"?"wss:":"ws:";
  var wsBase=wsScheme+"//"+location.host;
  // Absolute ws(s)://host/x: drop original host, use current host + prefix
  var m=u.match(/^wss?:\/\/[^\/]+(\/.*)?$/i);
  if(m){
    var path=m[1]||"/";
    if(path.indexOf(P)!==0)path=P+path.replace(/^\//,"");
    return wsBase+path;
  }
  // http(s)://host/x — promote to ws(s)
  if(/^https?:\/\//i.test(u)){
    try{var url=new URL(u);var path=url.pathname||"/";
      if(path.indexOf(P)!==0)path=P+path.replace(/^\//,"");
      return wsBase+path+(url.search||"");
    }catch(e){}
  }
  if(u.charAt(0)==="/")return wsBase+P+u.substring(1);
  return u;
}
// One-time cleanup: remove localStorage / sessionStorage entries whose value
// looks like a hardcoded URL pointing at a different host. SPAs (e.g.
// OpenCode) often persist the user-selected upstream base URL there, which
// will keep failing once the user switches to accessing via portly.
function cleanStorage(s){
  try{
    var rm=[];
    for(var i=0;i<s.length;i++){
      var k=s.key(i);var v=s.getItem(k);
      if(typeof v!=="string")continue;
      // crude heuristic: contains http(s)://host[:port] that isn't current origin
      var matches=v.match(/https?:\/\/[a-zA-Z0-9_.\-]+(:\d+)?/g);
      if(!matches)continue;
      var bad=matches.some(function(m){return m!==ORIGIN&&m.indexOf(ORIGIN)!==0;});
      if(bad)rm.push(k);
    }
    rm.forEach(function(k){try{s.removeItem(k);}catch(e){}});
    if(rm.length)console.info("[portly] cleaned",rm.length,"stale URL entries from",s===localStorage?"localStorage":"sessionStorage",rm);
  }catch(e){}
}
cleanStorage(localStorage);cleanStorage(sessionStorage);

var of=window.fetch;
if(of){window.fetch=function(i,o){
  if(typeof i==="string")i=fix(i);
  else if(i&&i.url){try{i=new Request(fix(i.url),i)}catch(e){}}
  return of.call(this,i,o);
};}
var oo=XMLHttpRequest.prototype.open;
XMLHttpRequest.prototype.open=function(m,u){
  arguments[1]=fix(u);return oo.apply(this,arguments);
};
var OW=window.WebSocket;
if(OW){window.WebSocket=function(u,p){return p===undefined?new OW(fixWS(u)):new OW(fixWS(u),p);};
  window.WebSocket.prototype=OW.prototype;
  for(var k in OW)try{window.WebSocket[k]=OW[k]}catch(e){}
}
var ES=window.EventSource;
if(ES){window.EventSource=function(u,c){return c===undefined?new ES(fix(u)):new ES(fix(u),c);};
  window.EventSource.prototype=ES.prototype;
}
var ps=history.pushState,rs=history.replaceState;
history.pushState=function(s,t,u){return ps.call(this,s,t,u==null?u:fix(u));};
history.replaceState=function(s,t,u){return rs.call(this,s,t,u==null?u:fix(u));};
})();</script>`
	return []byte(js)
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
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
