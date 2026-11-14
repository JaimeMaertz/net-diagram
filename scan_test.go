package netdiagram

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWebAnswers(t *testing.T) {
	// Any status counts: a 401 and a redirect are both a web server.
	for _, code := range []int{200, 302, 401, 404} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if code == 302 {
				w.Header().Set("Location", "https://192.0.2.1/")
			}
			w.WriteHeader(code)
		}))
		if !WebAnswers(srv.URL+"/", time.Second) {
			t.Errorf("status %d: not counted as a web server", code)
		}
		srv.Close()
	}

	// A self-signed certificate is accepted.
	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer tlsSrv.Close()
	if !WebAnswers(tlsSrv.URL+"/", time.Second) {
		t.Error("self-signed https: not counted as a web server")
	}

	// The phone-hotspot case: the connection is accepted and nothing ever answers.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			defer c.Close() // held open, silent
		}
	}()
	start := time.Now()
	if WebAnswers("http://"+ln.Addr().String()+"/", 300*time.Millisecond) {
		t.Error("silent listener counted as a web server")
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("silent listener: timeout not kept (%v)", d)
	}

	// A closed port.
	closed, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := closed.Addr().String()
	closed.Close()
	if WebAnswers("http://"+addr+"/", 300*time.Millisecond) {
		t.Error("closed port counted as a web server")
	}
}
