package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"

	"github.com/google/uuid"
	"golang.org/x/crypto/acme/autocert"
)

type Config struct {
	Backends      map[string]string `json:"backends"`
	Port          int               `json:"port"`
	Port2         int               `json:"port2"`
	SslCertPath   string            `json:"sslCertPath"`
	SslKeyPath    string            `json:"sslKeyPath"`
	HostWhitelist []string          `json:"hostWhitelist"`
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

			// X-Forwarded-For ヘッダーを更新または設定
			// クライアントのIPアドレスを取得
			clientIP := r.RemoteAddr
			if ip := strings.Split(clientIP, ":"); len(ip) > 0 {
				clientIP = ip[0]
			}
			if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
				clientIP = xff + ", " + clientIP
			}
			r.Header.Set("X-Forwarded-For", clientIP)

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
	if config.SslCertPath == "" || config.SslKeyPath == "" {
		fmt.Println("SSL Cert: Let's Encrypt")
		fmt.Println("certManager.....")
		certManager := autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			Cache:      autocert.DirCache("certs"),
			HostPolicy: autocert.HostWhitelist(config.HostWhitelist...), // 実際のドメイン名に置き換え
		}

		// HTTPサーバーを80番ポートで起動し、チャレンジリクエストを処理
		go func() {
			http.HandleFunc("/.well-known/acme-challenge/", func(w http.ResponseWriter, r *http.Request) {
				log.Printf("Received ACME challenge request for %s", r.URL.Path)
				certManager.HTTPHandler(nil).ServeHTTP(w, r)
			})
			log.Printf(fmt.Sprintf("Listening http on port :%d", config.Port2))
			err := http.ListenAndServe(fmt.Sprintf(":%d", config.Port2), nil)
			if err != nil {
				log.Fatalf("HTTP server for ACME challenge failed: %v", err)
			}
		}()

		// GetCertificate メソッドをラップしてログを追加
		getCertificate := func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			log.Printf("Attempting to get certificate for: %s", hello.ServerName)
			cert, err := certManager.GetCertificate(hello)
			if err != nil {
				log.Printf("Failed to get certificate for %s: %v", hello.ServerName, err)
			} else {
				log.Printf("Successfully got certificate for %s", hello.ServerName)
			}
			return cert, err
		}

		log.Println("https server.....")
		log.Printf(fmt.Sprintf("Listening https on port :%d", config.Port))
		server := &http.Server{
			Addr: fmt.Sprintf(":%d", config.Port),
			TLSConfig: &tls.Config{
				// GetCertificate: certManager.GetCertificate,
				GetCertificate: getCertificate,
			},
		}
		log.Fatal(server.ListenAndServeTLS("", "")) // Let's Encryptが自動的に証明書を管理
	} else {
		fmt.Println("SSL Cert: ", config.SslCertPath)
		log.Printf(fmt.Sprintf("Listening https on port :%d", config.Port))
		log.Fatal(http.ListenAndServeTLS(fmt.Sprintf(":%d", config.Port), config.SslCertPath, config.SslKeyPath, nil))
	}
}
