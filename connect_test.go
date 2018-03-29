package kiteconnect

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Test New Kite Connect instance
func TestNewClient(t *testing.T) {
	apiKey := "api_key"
	client := New(apiKey)

	if client.apiKey != apiKey {
		t.Errorf("Api key is not assigned properly.")
	}
}

// Test all client setters
func TestClientSetters(t *testing.T) {
	apiKey := "kitefront"
	client := New(apiKey)

	customDebug := true
	customBaseURI := "test"
	customTimeout := 1000 * time.Millisecond
	customAccessToken := "someaccesstoken"
	customHTTPClientTimeout := time.Duration(2000)
	customHTTPClient := &http.Client{
		Timeout: customHTTPClientTimeout,
	}

	// Set custom debug
	client.SetDebug(customDebug)
	if client.debug != customDebug {
		t.Errorf("Debug is not set properly.")
	}

	// Set custom base URI
	client.SetBaseURI(customBaseURI)
	if client.baseURI != customBaseURI {
		t.Errorf("Base URI is not set properly.")
	}

	// Set custom timeout for default http client
	client.SetTimeout(customTimeout)
	if client.httpClient.GetClient().Timeout != customTimeout {
		t.Errorf("HTTPClient timeout is not set properly.")
	}

	// Set access token
	client.SetAccessToken(customAccessToken)
	if client.accessToken != customAccessToken {
		t.Errorf("Access token is not set properly.")
	}

	// Set custom HTTP Client
	client.SetHTTPClient(customHTTPClient)
	if client.httpClient.GetClient() != customHTTPClient {
		t.Errorf("Custom HTTPClient is not set properly.")
	}

	// Set timeout for custo http client
	if client.httpClient.GetClient().Timeout != customHTTPClientTimeout {
		t.Errorf("Custom HTTPClient timeout is not set properly.")
	}

	// Set custom timeout for custom http client
	client.SetTimeout(customTimeout)
	if client.httpClient.GetClient().Timeout != customTimeout {
		t.Errorf("HTTPClient timeout is not set properly.")
	}
}

// Following boiler plate is used to implement setup/teardown using Go subtests feature

// Test only function prefix with this
const suiteTestMethodPrefix = "Test"

// TestSuite is an interface where you define suite and test case preparation and tear down logic.
type TestSuite struct{}

// Setup the API suit
func SetupAPITestSuit() {}

// TearDown API suit
func TearDownAPITestSuit() {}

// Individual test setup
func SetupAPITest() {}

// Individual test teardown
func TearDownAPITest() {}

/*
Run sets up the suite, runs its test cases and tears it down:
    1. Calls `suite.SetUpSuite`
    2. Seeks for any methods that have `Test` prefix, for each of them it:
      a. Calls `SetUp`
      b. Calls the test method itself
      c. Calls `TearDown`
    3. Calls `suite.TearDownSuite`
*/
func RunAPITests(t *testing.T, suite *TestSuite) {
	SetupAPITestSuit()
	defer TearDownAPITestSuit()

	suiteType := reflect.TypeOf(suite)
	for i := 0; i < suiteType.NumMethod(); i++ {
		m := suiteType.Method(i)
		if strings.HasPrefix(m.Name, suiteTestMethodPrefix) {
			t.Run(m.Name, func(t *testing.T) {
				SetupAPITest()
				defer TearDownAPITest()

				in := []reflect.Value{reflect.ValueOf(suite), reflect.ValueOf(t)}
				m.Func.Call(in)
			})
		}
	}
}

func TestAPIMethods(t *testing.T) {
	s := &TestSuite{}
	RunAPITests(t, s)
}
