package proxy

import (
	"errors"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var jwtSecret = []byte(os.Getenv("JWT_SECRET"))

func extractUserIDFromJWT(
	authHeader string,
	cache *JWTCache) (*CachedJWT, error) {
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || parts[0] != "Bearer" {
		return nil, errors.New("invalid auth header")
	}

	tokenStr := parts[1]

	if cached, ok := cache.Get(tokenStr); ok {
		return cached, nil
	}

	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		// Ensure HMAC signing
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return jwtSecret, nil
	})
	if err != nil || !token.Valid {
		return nil, errors.New("invalid token")
	}

	claims := token.Claims.(jwt.MapClaims)

	userID, _ := claims["userId"].(string)
	expUnix, _ := claims["exp"].(float64)

	cached := &CachedJWT{
		UserID: userID,
		Exp:    time.Unix(int64(expUnix), 0),
	}

	cache.Set(tokenStr, cached)
	return cached, nil
}
