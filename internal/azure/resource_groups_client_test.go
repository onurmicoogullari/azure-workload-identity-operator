package azure

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/arm"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
)

type staticTokenCredential struct{}

func (staticTokenCredential) GetToken(context.Context, policy.TokenRequestOptions) (azcore.AccessToken, error) {
	return azcore.AccessToken{Token: "test-token", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

type successfulDeleteTransport struct {
	calls int
}

func (t *successfulDeleteTransport) Do(request *http.Request) (*http.Response, error) {
	t.calls++
	if t.calls == 1 {
		header := make(http.Header)
		header.Set("Azure-AsyncOperation", "https://management.azure.com/providers/Microsoft.Resources/operations/test-operation?api-version=2021-04-01")
		header.Set("Retry-After", "0")
		return &http.Response{
			StatusCode: http.StatusAccepted,
			Header:     header,
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    request,
		}, nil
	}
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     header,
		Body:       io.NopCloser(strings.NewReader(`{"status":"Succeeded"}`)),
		Request:    request,
	}, nil
}

func TestAzureResourceGroupsClientDeleteCompletesPoller(t *testing.T) {
	transport := &successfulDeleteTransport{}
	client, err := armresources.NewResourceGroupsClient("00000000-0000-0000-0000-000000000000", staticTokenCredential{}, &arm.ClientOptions{
		ClientOptions: policy.ClientOptions{Transport: transport},
	})
	if err != nil {
		t.Fatalf("create resource groups client: %v", err)
	}

	wrapped := &azureResourceGroupsClient{ResourceGroupsClient: client}
	if err := wrapped.Delete(context.Background(), "rg-test"); err != nil {
		t.Fatalf("delete resource group: %v", err)
	}
	if transport.calls != 2 {
		t.Fatalf("transport calls = %d, want 2", transport.calls)
	}
}
