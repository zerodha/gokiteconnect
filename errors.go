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
	TPINAuthError   = "TPINAuthException"
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
	err := Error{}
	err.Message = message
	err.ErrorType = etype
	err.Data = data

	switch etype {
	case GeneralError:
		err.Code = http.StatusInternalServerError
	case TokenError:
		err.Code = http.StatusForbidden
	case PermissionError:
		err.Code = http.StatusForbidden
	case UserError:
		err.Code = http.StatusForbidden
	case TwoFAError:
		err.Code = http.StatusForbidden
	case OrderError:
		err.Code = http.StatusBadRequest
	case InputError:
		err.Code = http.StatusBadRequest
	case DataError:
		err.Code = http.StatusGatewayTimeout
	case NetworkError:
		err.Code = http.StatusServiceUnavailable
	case TPINAuthError:
		err.Code = http.StatusPreconditionRequired
	default:
		err.Code = http.StatusInternalServerError
		err.ErrorType = GeneralError
	}

	return err
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
	case http.StatusPreconditionRequired:
		err = TPINAuthError
	default:
		err = GeneralError
	}

	return err
}
