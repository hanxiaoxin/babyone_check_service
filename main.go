package main

import (
	"babyone_check_service/internal/monitor"
	"context"
	"errors"
	"github.com/joho/godotenv"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)

func main() {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal("invalid .env configuration")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db := os.Getenv("DB_PATH")
	if db == "" {
		db = "monitor.db"
	}
	s, err := monitor.Open(db)
	if err != nil {
		log.Fatal(err)
	}
	defer s.Close()
	if err = s.Seed(); err != nil {
		log.Fatal(err)
	}
	if err = s.ConfigureNotifications(monitor.SMTPConfig{Port: 587, TLSMode: "starttls"}, false); err != nil {
		log.Fatal(err)
	}
	authEnabled := true
	if value := os.Getenv("API_AUTH_ENABLED"); value != "" {
		authEnabled, err = strconv.ParseBool(value)
		if err != nil {
			log.Fatal("API_AUTH_ENABLED must be true or false")
		}
	}
	token := os.Getenv("API_TOKEN")
	if authEnabled && token == "" {
		log.Fatal("API_TOKEN is required")
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = "0.0.0.0:8080"
	}
	schedulerDone := make(chan struct{})
	go func() { defer close(schedulerDone); s.Run(ctx) }()
	server := &http.Server{Addr: addr, Handler: s.Router(token, os.Getenv("CORS_ORIGIN"), authEnabled), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		server.Shutdown(c)
	}()
	log.Printf("listening on %s", addr)
	defer func() { stop(); <-schedulerDone }()
	if err = server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
