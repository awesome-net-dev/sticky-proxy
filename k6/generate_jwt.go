package main

import (
	"encoding/csv"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	const totalUsers = 1000
	secret := []byte(getEnv("JWT_SECRET", "mysecret"))

	file, err := os.Create("users.csv")
	if err != nil {
		slog.Error("failed to create output file", "error", err)
		os.Exit(1)
	}
	defer file.Close()

	writer := csv.NewWriter(file)
	defer writer.Flush()

	writer.Write([]string{"userId", "jwt"})

	for i := 0; i < totalUsers; i++ {
		claims := jwt.MapClaims{
			"userId": i,
			"exp":    time.Now().Add(8760 * time.Hour).Unix(), // 1 year
		}
		token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
		signed, err := token.SignedString(secret)
		if err != nil {
			slog.Error("failed to sign token", "userId", i, "error", err)
			os.Exit(1)
		}

		if err := writer.Write([]string{fmt.Sprintf("%d", i), signed}); err != nil {
			slog.Error("failed to write csv row", "userId", i, "error", err)
			os.Exit(1)
		}

		if i%10000 == 0 {
			slog.Info("generating jwts", "progress", i, "total", totalUsers)
		}
	}

	slog.Info("jwt generation complete", "file", "users.csv", "totalUsers", totalUsers)
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
