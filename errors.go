/* Package common contains common generic structures
 * like errors that can be used across other kite packages.
 */

package kiteconnect

import "net/http"

// API errors. Check documantation to learn about individual exception: https://kite.trade/docs/connect/v3/exceptions/.
const (
	GeneralError    = "GeneralException"
	TokenError      = "TokenException"
	PermissionError = "PermissionError"
	UserError       = "UserException"
	TwoFAError      = "TwoFAException"
	OrderError      = "OrderException"
	InputError      = "InputException"
	DataError       = "DataException"
	NetworkError    = "NetworkException"
)

// Error is the error type used for all API errors.
type Error struct {
	Code      int
	ErrorType string
	Message   string
	Data      interface{}
}

// This makes Error a valid Go error type.
func (e Error) Error() string {
	return e.Message
}

// NewError creates and returns a new instace of Error
// with custom error metadata.
func NewError(etype string, message string, data interface{}) error {
	var (
		code = http.StatusInternalServerError
	)

	switch etype {
	case GeneralError:
		code = http.StatusInternalServerError
	case TokenError:
		code = http.StatusForbidden
	case PermissionError:
		code = http.StatusForbidden
	case UserError:
		code = http.StatusForbidden
	case TwoFAError:
		code = http.StatusForbidden
	case OrderError:
		code = http.StatusBadRequest
	case InputError:
		code = http.StatusBadRequest
	case DataError:
		code = http.StatusGatewayTimeout
	case NetworkError:
		code = http.StatusServiceUnavailable
	default:
		code = http.StatusInternalServerError
		etype = GeneralError
	}

	return newError(etype, message, code, data)
}

func newError(etype, message string, code int, data interface{}) Error {
	return Error{
		Message:   message,
		ErrorType: etype,
		Data:      data,
		Code:      code,
	}
}

// GetErrorName returns an error name given an HTTP code.
func GetErrorName(code int) string {
	var err string

	switch code {
	case http.StatusInternalServerError:
		err = GeneralError
	case http.StatusForbidden, http.StatusUnauthorized:
		err = TokenError
	case http.StatusBadRequest:
		err = InputError
	case http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		err = NetworkError
	default:
		err = GeneralError
	}

	return err
}
