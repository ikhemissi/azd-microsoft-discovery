package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

const ARMVersion = "2026-06-01"
const AgentVersion = "2026-02-01-preview"
const ARMEndpoint = "https://management.azure.com"
const maxBody = 4 << 20

type Client struct {
	Credential        azcore.TokenCredential
	HTTP              *http.Client
	WorkspaceEndpoint string
	bookshelfEndpoint string
	Sleep             func(context.Context, time.Duration) error
	Progress          func(string)
}

type Resource struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Location   string `json:"location"`
	Properties struct {
		ProvisioningState string         `json:"provisioningState"`
		Version           string         `json:"version"`
		Definition        map[string]any `json:"definitionContent"`
		BookshelfURI      string         `json:"bookshelfUri"`
	} `json:"properties"`
}

type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

type HTTPError struct {
	Status    int
	Code      string
	RequestID string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("Discovery request failed (HTTP %d, code=%s, requestId=%s); check resource configuration and access", e.Status, e.Code, e.RequestID)
}

// Rejected excludes timeouts, conflicts, throttling, and server errors whose
// mutation outcome cannot safely be inferred from the HTTP status alone.
func (e *HTTPError) Rejected() bool {
	switch e.Status {
	case 400, 401, 403, 404, 405, 422:
		return true
	default:
		return false
	}
}

type OperationError struct{ State string }

func (e *OperationError) Error() string { return "Discovery operation ended with " + e.State }

var diagnosticIdentifier = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)

func safeIdentifier(value string) string {
	if !diagnosticIdentifier.MatchString(value) {
		return "unavailable"
	}
	return value
}

func NewClient(credential azcore.TokenCredential) *Client {
	return &Client{
		Credential: credential,
		HTTP: &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("authenticated redirects are not supported")
		}},
		Sleep: sleep,
	}
}

func sleep(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) ValidateURL(value string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Port() != "" {
		return nil, errors.New("untrusted Discovery request/operation URL")
	}
	arm, _ := url.Parse(ARMEndpoint)
	workspace, err := url.Parse(c.WorkspaceEndpoint)
	bookshelf, bookshelfErr := url.Parse(c.bookshelfEndpoint)
	if u.Host != arm.Host &&
		(err != nil || workspace == nil || workspace.Host == "" || u.Host != workspace.Host) &&
		(bookshelfErr != nil || bookshelf == nil || bookshelf.Host == "" || u.Host != bookshelf.Host) {
		return nil, errors.New("Discovery request/operation URL is not a verified destination")
	}
	return u, nil
}

func (c *Client) Request(ctx context.Context, method, address string, body any) (*Response, error) {
	u, err := c.ValidateURL(address)
	if err != nil {
		return nil, err
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("serialize Discovery request: %w", err)
		}
		if len(data) > maxBody {
			return nil, errors.New("Discovery request exceeds size limit")
		}
	}
	scope := "https://management.azure.com/.default"
	if u.Host != "management.azure.com" {
		scope = "https://discovery.azure.com/.default"
	}
	for attempt := 0; ; attempt++ {
		token, err := c.Credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{scope}})
		if err != nil {
			return nil, errors.New("cannot obtain azd credential for the required Discovery audience")
		}
		request, err := http.NewRequestWithContext(ctx, method, address, bytes.NewReader(data))
		if err != nil {
			return nil, errors.New("cannot construct Discovery request")
		}
		request.Header.Set("Authorization", "Bearer "+token.Token)
		request.Header.Set("Content-Type", "application/json")
		// Copy to enforce redirects even if an injected client forgot to configure them.
		client := *c.HTTP
		client.CheckRedirect = func(*http.Request, []*http.Request) error {
			return errors.New("Discovery redirects are prohibited")
		}
		response, err := client.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, errors.New("Discovery transport failed; inspect remote state before retrying a mutation")
		}
		content, readErr := io.ReadAll(io.LimitReader(response.Body, maxBody+1))
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			return nil, errors.New("cannot read Discovery response; mutation outcome may be ambiguous")
		}
		if len(content) > maxBody {
			return nil, errors.New("Discovery response exceeds size limit")
		}
		result := &Response{Status: response.StatusCode, Header: response.Header, Body: content}
		if method == http.MethodGet && attempt < 3 &&
			(response.StatusCode == 429 || response.StatusCode >= 500) {
			if err := c.Sleep(ctx, retryDelay(response.Header, attempt)); err != nil {
				return nil, err
			}
			continue
		}
		if result.Status < 200 || result.Status >= 300 {
			var failure struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			// Error messages can contain request bodies or credentials. Only expose
			// bounded identifiers, never the server's free-form message.
			if json.Unmarshal(content, &failure) != nil {
				failure.Error.Code = ""
			}
			return nil, &HTTPError{Status: result.Status, Code: safeIdentifier(failure.Error.Code),
				RequestID: safeIdentifier(response.Header.Get("x-ms-request-id"))}
		}
		return result, nil
	}
}

func retryDelay(header http.Header, attempt int) time.Duration {
	if value := header.Get("Retry-After"); value != "" {
		if seconds, err := strconv.Atoi(value); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
		if deadline, err := http.ParseTime(value); err == nil {
			return max(time.Until(deadline), time.Duration(0))
		}
	}
	return time.Second * time.Duration(1<<min(attempt, 4))
}

func ARMURL(id string) string { return ARMEndpoint + id + "?api-version=" + ARMVersion }

func (c *Client) Resource(ctx context.Context, id string) (*Resource, error) {
	response, err := c.Request(ctx, http.MethodGet, ARMURL(id), nil)
	if err != nil {
		return nil, fmt.Errorf("read Discovery resource %s: %w", id, err)
	}
	var resource Resource
	if err := json.Unmarshal(response.Body, &resource); err != nil {
		return nil, errors.New("invalid Discovery ARM resource response")
	}
	if !strings.EqualFold(resource.ID, id) {
		return nil, errors.New("Discovery ARM readback resource ID mismatch")
	}
	return &resource, nil
}

func (c *Client) Ready(ctx context.Context, id string) (*Resource, error) {
	if c.Progress != nil {
		c.Progress("Verifying Discovery resource readiness")
	}
	for attempt := 0; ; attempt++ {
		resource, err := c.Resource(ctx, id)
		if err != nil {
			return nil, err
		}
		done, err := Terminal(resource.Properties.ProvisioningState)
		if err != nil {
			return resource, err
		}
		if done {
			return resource, nil
		}
		if err := c.Sleep(ctx, retryDelay(nil, attempt)); err != nil {
			return nil, fmt.Errorf("Discovery readiness interrupted; Azure operation was not canceled: %w", err)
		}
	}
}

func Terminal(state string) (bool, error) {
	switch state {
	case "Succeeded":
		return true, nil
	case "Failed", "Canceled", "Cancelled":
		return false, &OperationError{State: state}
	case "Accepted", "Creating", "Updating", "Running", "InProgress", "Provisioning":
		return false, nil
	default:
		return false, errors.New("Discovery response is missing a recognized authoritative status")
	}
}

func (c *Client) Poll(ctx context.Context, address string) error {
	if c.Progress != nil {
		c.Progress("Waiting for Discovery operation")
	}
	for attempt := 0; ; attempt++ {
		response, err := c.Request(ctx, http.MethodGet, address, nil)
		if err != nil {
			return err
		}
		var operation struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(response.Body, &operation); err != nil {
			return errors.New("invalid Discovery operation response")
		}
		done := false
		if operation.Status != "NotStarted" {
			done, err = Terminal(operation.Status)
		}
		if err != nil || done {
			return err
		}
		if err := c.Sleep(ctx, retryDelay(response.Header, attempt)); err != nil {
			return fmt.Errorf("operation interrupted; inspect/resume remote state, not a new submission: %w", err)
		}
	}
}

func (c *Client) Operation(response *Response, base string) (string, error) {
	location := response.Header.Get("Operation-Location")
	if location == "" {
		location = response.Header.Get("Azure-AsyncOperation")
	}
	if location == "" {
		if response.Status == http.StatusAccepted {
			return "", errors.New("accepted Discovery operation has no verifiable operation URL")
		}
		return "", nil
	}
	root, err := url.Parse(base)
	if err != nil {
		return "", errors.New("invalid operation base")
	}
	relative, err := url.Parse(location)
	if err != nil {
		return "", errors.New("invalid operation URL")
	}
	address := root.ResolveReference(relative).String()
	if _, err := c.ValidateURL(address); err != nil {
		return "", err
	}
	return address, nil
}
