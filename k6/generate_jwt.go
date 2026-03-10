package main

import (
	"encoding/csv"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	totalUsers, err := strconv.Atoi(getEnv("TOTAL_USERS", "1000"))
	if err != nil {
		slog.Error("invalid TOTAL_USERS", "error", err)
		os.Exit(1)
	}
	secret := []byte(getEnv("JWT_SECRET", "mysecret"))

	file, err := os.Create("users.csv")
	if err != nil {
		slog.Error("failed to create output file", "error", err)
		os.Exit(1)
	}
	writer := csv.NewWriter(file)

	_ = writer.Write([]string{"sub", "jwt"})

	for i := 0; i < totalUsers; i++ {
		sub := strconv.Itoa(i)
		claims := jwt.MapClaims{
			"sub": sub,
			"exp": time.Now().Add(8760 * time.Hour).Unix(), // 1 year
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString(secret)
		if err != nil {
			slog.Error("failed to sign token", "sub", sub, "error", err)
			writer.Flush()
			_ = file.Close()
			os.Exit(1)
		}

		if err := writer.Write([]string{sub, signed}); err != nil {
			slog.Error("failed to write csv row", "sub", sub, "error", err)
			writer.Flush()
			_ = file.Close()
			os.Exit(1)
		}

		if i%10000 == 0 {
			slog.Info("generating jwts", "progress", i, "total", totalUsers)
		}
	}

	writer.Flush()
	_ = file.Close()
	slog.Info("jwt generation complete", "file", "users.csv", "totalUsers", totalUsers)
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
