/*
 * Copyright The RAI Inc.
 * The RAI Authors
 */

package helpers

import (
	"errors"

	"github.com/golang-jwt/jwt"
	"github.com/labstack/echo/v4"
	"github.com/spf13/viper"
)

/*
 * ExtractUserInfoFromJWT extracts user info from JWT. It is faster than calling redis to get those info.
 */
func ExtractUserInfoFromJWT(c echo.Context, claims jwt.Claims) error {

	tokenString := ExtractJWTFromHeader(c)
	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		return []byte(viper.GetString("jwt.secret")), nil
	})

	if err != nil {
		return errors.New("invalid user token")
	}

	if token.Valid {

		return nil

	} else if ve, ok := err.(*jwt.ValidationError); ok {

		if ve.Errors&jwt.ValidationErrorMalformed != 0 {
			return errors.New("invalid user token")

		} else if ve.Errors&(jwt.ValidationErrorExpired|jwt.ValidationErrorNotValidYet) != 0 {
			return errors.New("token is expired")

		} else {
			return errors.New("invalid user token")
		}

	} else {
		return errors.New("invalid user token")
	}
}

// ExtractJWTFromHeader returns the JWT token string from authorization header.
func ExtractJWTFromHeader(c echo.Context) string {

	var tokenString string

	authHeader := c.Request().Header.Get("Authorization")

	l := len("Bearer")

	if len(authHeader) > l+1 && authHeader[:l] == "Bearer" {
		tokenString = authHeader[l+1:]
	} else {
		tokenString = ""
	}

	return tokenString
}

func EncodeJWT(claims jwt.Claims) (string, error) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	// Generate encoded token and send it as response.
	return token.SignedString([]byte(viper.GetString("jwt.secret")))
}
