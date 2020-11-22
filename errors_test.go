package kiteconnect

import (
	"net/http"
	"testing"
)

func TestGetErrorName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		code int
		want string
	}{
		{
			name: "Internal Server Error",
			code: http.StatusInternalServerError,
			want: GeneralError,
		},
		{
			name: "Internal Server Error",
			code: http.StatusInternalServerError,
			want: GeneralError,
		},
		{
			name: "Status Unauthorized",
			code: http.StatusForbidden,
			want: TokenError,
		},
		{
			name: "Internal Server Error",
			code: http.StatusUnauthorized,
			want: TokenError,
		},
		{
			name: "Bad Request",
			code: http.StatusBadRequest,
			want: InputError,
		},
		{
			name: "Service Unavailable",
			code: http.StatusServiceUnavailable,
			want: NetworkError,
		},
		{
			name: "Gateway Timeout",
			code: http.StatusGatewayTimeout,
			want: NetworkError,
		},
		{
			name: "Other Timeout",
			code: -1,
			want: GeneralError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := GetErrorName(tt.code); got != tt.want {
				t.Errorf("GetErrorName() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestError_Error(t *testing.T) {
	t.Parallel()
	type fields struct {
		ErrorType string
		Message   string
	}
	tests := []struct {
		name   string
		fields fields
		want   string
	}{
		{
			name: "TestError",
			fields: fields{
				ErrorType: GeneralError,
				Message:   "TestError",
			},
			want: "TestError",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewError(tt.fields.ErrorType, tt.fields.Message, nil)
			if got := e.Error(); got != tt.want {
				t.Errorf("Error.Error() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNewError(t *testing.T) {
	t.Parallel()
	type args struct {
		etype string
	}
	tests := []struct {
		name string
		args args
		want int
	}{
		{
			name: "General Error",
			args: args{
				etype: GeneralError,
			},
			want: http.StatusInternalServerError,
		},
		{
			name: "Token Error",
			args: args{
				etype: TokenError,
			},
			want: http.StatusForbidden,
		},
		{
			name: "Permission Error",
			args: args{
				etype: PermissionError,
			},
			want: http.StatusForbidden,
		},
		{
			name: "User Error",
			args: args{
				etype: UserError,
			},
			want: http.StatusForbidden,
		},
		{
			name: "2FA Error",
			args: args{
				etype: TwoFAError,
			},
			want: http.StatusForbidden,
		},
		{
			name: "Order Error",
			args: args{
				etype: OrderError,
			},
			want: http.StatusBadRequest,
		},
		{
			name: "Input Error",
			args: args{
				etype: InputError,
			},
			want: http.StatusBadRequest,
		},
		{
			name: "Data Error",
			args: args{
				etype: DataError,
			},
			want: http.StatusGatewayTimeout,
		},
		{
			name: "Network Error",
			args: args{
				etype: NetworkError,
			},
			want: http.StatusServiceUnavailable,
		},
		{
			name: "Default Error",
			args: args{
				etype: "Unknown Error",
			},
			want: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := NewError(tt.args.etype, "Test Error", nil)
			if err, ok := e.(Error); !ok {
				t.Errorf("NewError() does not implement Error error = %v", e)
			} else if err.Code != tt.want {
				t.Errorf("NewError() error = %v, wantErr %v", err.Code, tt.want)
			}
		})
	}
}
