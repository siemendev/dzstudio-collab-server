// Package httpapi serves a core.Service over HTTP the way the game needs it: always HTTP 200, the marker line and
// one JSON object, errors in the body, the body read as JSON whatever the content type says.
package httpapi

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/siemendev/dzstudio-collab-server/core"
	p "github.com/siemendev/dzstudio-collab-server/protocol"
)

// Extension answers extra routes below the base address (login page, test controls). It returns true if it
// answered.
type Extension interface {
	HandleHTTP(w http.ResponseWriter, r *http.Request, subpath string, body []byte, remote string) bool
}

type Options struct {
	Service    *core.Service
	BasePath   string // e.g. /dzcollab/
	PublicURL  string // public base address for links; derived from the request when empty
	TrustProxy bool   // trust X-Forwarded-For / -Proto / -Host
	Extensions []Extension
}

func NormalizeBasePath(path string) string {
	path = strings.TrimSpace(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path
}

// Send writes a protocol answer.
func Send(w http.ResponseWriter, o *p.Obj) {
	data := p.Encode(o)
	h := w.Header()
	h.Set("content-type", "text/plain; charset=utf-8")
	h.Set("cache-control", "no-store")
	h.Set("x-content-type-options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func Handler(opts Options) http.Handler {
	base := NormalizeBasePath(opts.BasePath)
	limit := opts.Service.Limits().MaxRequestBytes
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
		if err != nil {
			Send(w, p.NewError(p.BadRequest, "could not read the request").Body())
			return
		}
		if int64(len(body)) > limit {
			_, _ = io.Copy(io.Discard, r.Body)
			Send(w, p.NewError(p.TooLarge, "request too large", "maxBytes", limit).Body())
			return
		}
		path := r.URL.Path
		if !strings.HasPrefix(path, base) && path+"/" != base {
			Send(w, p.NewError(p.BadRequest, "endpoints live below "+base).Body())
			return
		}
		sub := ""
		if len(path) > len(base) {
			sub = strings.Trim(path[len(base):], "/")
		}
		remote := remoteAddress(r, opts.TrustProxy)
		for _, ext := range opts.Extensions {
			if ext.HandleHTTP(w, r, sub, body, remote) {
				return
			}
		}
		ctx := core.RequestContext{RemoteAddress: remote, BaseURL: baseURL(r, base, opts)}
		switch r.Method {
		case http.MethodGet, http.MethodHead:
			if sub != "info" {
				Send(w, p.NewError(p.BadRequest, "only info answers GET").Body())
				return
			}
			Send(w, opts.Service.Dispatch("info", map[string]any{}, ctx))
			return
		case http.MethodPost:
		default:
			Send(w, p.NewError(p.BadRequest, "use GET or POST").Body())
			return
		}
		if sub == "info" && strings.TrimSpace(string(body)) == "" {
			Send(w, opts.Service.Dispatch("info", map[string]any{}, ctx))
			return
		}
		m, err := p.ParseBody(body)
		if err != nil {
			var pe *p.Error
			if errors.As(err, &pe) {
				Send(w, pe.Body())
			} else {
				Send(w, p.NewError(p.BadRequest, "body is not JSON").Body())
			}
			return
		}
		Send(w, opts.Service.Dispatch(sub, m, ctx))
	})
}

func remoteAddress(r *http.Request, trust bool) string {
	if trust {
		if fwd := strings.TrimSpace(strings.Split(r.Header.Get("x-forwarded-for"), ",")[0]); fwd != "" {
			return fwd
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func baseURL(r *http.Request, base string, opts Options) string {
	if opts.PublicURL != "" {
		if strings.HasSuffix(opts.PublicURL, "/") {
			return opts.PublicURL
		}
		return opts.PublicURL + "/"
	}
	proto, host := "http", r.Host
	if r.TLS != nil {
		proto = "https"
	}
	if opts.TrustProxy {
		if v := strings.TrimSpace(strings.Split(r.Header.Get("x-forwarded-proto"), ",")[0]); v != "" {
			proto = v
		}
		if v := strings.TrimSpace(strings.Split(r.Header.Get("x-forwarded-host"), ",")[0]); v != "" {
			host = v
		}
	}
	if host == "" {
		host = "localhost"
	}
	return proto + "://" + host + base
}
