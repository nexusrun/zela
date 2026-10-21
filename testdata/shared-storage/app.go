// Disposable acceptance-test app. Its only persistent file is /data/acceptance-data.
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "health" {
		client := &http.Client{Timeout: time.Second}
		resp, err := client.Get("http://127.0.0.1:8080/healthz")
		if err != nil {
			os.Exit(1)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			os.Exit(1)
		}
		return
	}
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if _, err := os.Stat("/data/.zela-identity"); err != nil {
			http.Error(w, "storage unavailable", 503)
			return
		}
		w.WriteHeader(200)
	})
	http.HandleFunc("/file", func(w http.ResponseWriter, r *http.Request) {
		const name = "/data/acceptance-data"
		if r.Method == "PUT" {
			data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
			if err != nil {
				http.Error(w, "invalid payload", 400)
				return
			}
			if err = os.WriteFile(name, data, 0600); err != nil {
				http.Error(w, "write failed", 500)
				return
			}
			w.WriteHeader(204)
			return
		}
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		data, err := os.ReadFile(name)
		if err != nil {
			http.Error(w, "read failed", 500)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(data)
	})
	server := &http.Server{Addr: ":8080", ReadHeaderTimeout: 5 * time.Second}
	if err := server.ListenAndServe(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
