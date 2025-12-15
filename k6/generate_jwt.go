package main

import (
	"encoding/csv"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func main() {
	const totalUsers = 1000
	secret := []byte(getEnv("JWT_SECRET", "mysecret"))

	file, err := os.Create("users.csv")
	if err != nil {
		log.Fatal(err)
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
			log.Fatal(err)
		}

		if err := writer.Write([]string{fmt.Sprintf("%d", i), signed}); err != nil {
			log.Fatal(err)
		}

		if i%10000 == 0 {
			fmt.Printf("Generated JWTs for %d users...\n", i)
		}
	}

	fmt.Println("All 100k JWTs generated in users.csv")
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}
