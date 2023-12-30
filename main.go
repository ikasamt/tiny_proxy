package main

import (
	"context"
	"encoding/json"
	"github.com/google/uuid"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
)

type Config struct {
	Backends    map[string]string `json:"backends"`
	Port        int               `json:"port"`
	SslCertPath string            `json:"sslCertPath"`
	SslKeyPath  string            `json:"sslKeyPath"`
}

var config Config
var proxies = map[string]*httputil.ReverseProxy{}

func loadConfigJson() {
	// 設定を読み込む処理をここに追加
	// config.jsonを読み込んでbackendsに設定を追加する
	bytes_, err := os.ReadFile("config.json")
	if err != nil {
		panic(err)
	}

	// 設定をパースする
	err = json.Unmarshal(bytes_, &config)
	if err != nil {
		panic(err)
	}

	// 各ルートの設定
	for key, value := range config.Backends {
		proxyURL, err := url.Parse(value)
		if err != nil {
			log.Fatal(err)
		}

		proxy := httputil.NewSingleHostReverseProxy(proxyURL)
		proxy.ModifyResponse = func(response *http.Response) error {
			response.Header.Set("X-Your-Custom-Header", "Value")
			return nil
		}
		proxies[key] = proxy
	}
}

// レスポンスをラップするための構造体
type loggingResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

// WriteHeader をオーバーライドしてステータスコードをキャプチャ
func (lrw *loggingResponseWriter) WriteHeader(code int) {
	lrw.statusCode = code
	lrw.ResponseWriter.WriteHeader(code)
}

func main() {
	fp, err := os.OpenFile("access.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		panic(err)
	}
	logger := slog.New(slog.NewJSONHandler(fp, nil))
	slog.SetDefault(logger)

	log.SetFlags(log.Lshortfile | log.LstdFlags)
	loadConfigJson()

	http.HandleFunc("/_/reload", func(w http.ResponseWriter, r *http.Request) {
		loadConfigJson()
		w.Write([]byte("ok"))
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		for key := range proxies {
			host := r.Host

			// クッキーからUUIDを取得、なければ新しいUUIDを生成して設定
			uuidCookie, err := r.Cookie("user_uuid")
			if err != nil {
				newUUID := uuid.New().String()
				http.SetCookie(w, &http.Cookie{Name: "user_uuid", Value: newUUID, Path: "/"})
				uuidCookie = &http.Cookie{Value: newUUID}
			}

			lrw := &loggingResponseWriter{ResponseWriter: w, statusCode: http.StatusOK}
			if strings.HasPrefix(host, key) {
				proxy := proxies[key]
				proxy.ServeHTTP(lrw, r)
				slog.LogAttrs(
					context.Background(),
					slog.LevelInfo,
					"",
					slog.String("uuid", uuidCookie.Value),
					slog.String("remote_addr", r.RemoteAddr),
					slog.String("method", r.Method),
					slog.String("host", r.Host),
					slog.String("path", r.URL.Path),
					slog.Int("status", lrw.statusCode),
				)
				return
			}
		}
	})

	log.Println("log file: access.log")
	log.Printf("Listening on port 8080")
	log.Fatal(http.ListenAndServeTLS(":8080", config.SslCertPath, config.SslKeyPath, nil))
}
