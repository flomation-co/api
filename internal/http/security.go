package http

import (
	"time"

	"flomation.app/automate/api/internal/config"
	"github.com/dgrijalva/jwt-go"
	"github.com/pkg/errors"
)

func ParseToken(cfg *config.Config, token string) (*string, error) {
	tkn, err := jwt.Parse(token, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, nil
		}

		return []byte(cfg.Security.Secret), nil
	})
	if err != nil {
		return nil, err
	}

	if claims, ok := tkn.Claims.(jwt.MapClaims); ok && tkn.Valid {
		now := time.Now().UTC().Unix()
		expiry := int64(claims["exp"].(float64))
		nbf := int64(claims["nbf"].(float64))

		if now > expiry {
			return nil, errors.New("token expired")
		}

		if now < nbf {
			return nil, errors.New("token not yet valid")
		}

		jwtID := string(claims["id"].(string))
		return &jwtID, nil
	}

	return nil, errors.New("token invalid")
}
