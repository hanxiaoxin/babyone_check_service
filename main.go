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
	smtpConfig, err := monitor.SMTPFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	initialNotify := false
	if value := os.Getenv("AUTO_NOTIFY"); value != "" {
		initialNotify, err = strconv.ParseBool(value)
		if err != nil {
			log.Fatal("AUTO_NOTIFY must be true or false")
		}
	}
	if err = s.ConfigureNotifications(smtpConfig, initialNotify); err != nil {
		log.Fatal(err)
	}
	token := os.Getenv("API_TOKEN")
	if token == "" {
		log.Fatal("API_TOKEN is required")
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	schedulerDone := make(chan struct{})
	go func() { defer close(schedulerDone); s.Run(ctx) }()
	server := &http.Server{Addr: addr, Handler: s.Router(token, os.Getenv("CORS_ORIGIN")), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
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
