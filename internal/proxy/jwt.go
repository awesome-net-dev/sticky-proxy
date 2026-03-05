package proxy

import (
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func extractUserIDFromJWT(
	authHeader string,
	cache *JWTCache,
	jwtSecret []byte,
	routingClaim string) (*CachedJWT, error) {
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

	routingKey, _ := claims[routingClaim].(string)
	if routingKey == "" {
		return nil, errors.New("routing claim is empty or missing")
	}
	expUnix, _ := claims["exp"].(float64)

	cached := &CachedJWT{
		RoutingKey: routingKey,
		Exp:        time.Unix(int64(expUnix), 0),
	}

	cache.Set(tokenStr, cached)
	return cached, nil
}
