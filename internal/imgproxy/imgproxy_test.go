package imgproxy

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProxy(t *testing.T) {
	images := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/page" {
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<p>hi</p>"))
			return
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Referer") != "" {
			t.Error("cookie or referrer forwarded")
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("png"))
	}))
	defer images.Close()

	get := func(p *Proxy, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", URL(target), nil)
		req.Header.Set("Cookie", "mail_session=secret")
		p.ServeHTTP(rec, req)
		return rec
	}

	strict := New(false)
	for _, target := range []string{images.URL + "/a.png", "http://localhost:1/a.png", "http://169.254.169.254/latest", "file:///etc/passwd", "ftp://x/y"} {
		if rec := get(strict, target); rec.Code < 400 {
			t.Errorf("%s: got %d", target, rec.Code)
		}
	}

	local := New(true)
	if rec := get(local, images.URL+"/a.png"); rec.Code != 200 || rec.Body.String() != "png" || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("image: %d %q", rec.Code, rec.Body.String())
	}
	if rec := get(local, images.URL+"/page"); rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("html page: %d", rec.Code)
	}
}
